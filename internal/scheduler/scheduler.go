// Package scheduler implements FR-SE-06: background sync on a per-account
// cron schedule, with bounded concurrency across accounts.
//
// A Scheduler only ever exists once the vault is unlocked — the caller
// (internal/httpapi) constructs one from already-decrypted dependencies
// and Starts it right after unlock. Locked-state gating (поправка #3:
// "Scheduler ... полностью остановлен" until Master Key is supplied) falls
// out of that lifecycle for free, rather than needing its own pause flag.
package scheduler

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/panjf2000/ants/v2"
	"github.com/robfig/cron/v3"
	"go.uber.org/zap"

	"github.com/yurydemin/marchi/internal/account"
	"github.com/yurydemin/marchi/internal/config"
	"github.com/yurydemin/marchi/internal/db/repo"
	"github.com/yurydemin/marchi/internal/db/writer"
	"github.com/yurydemin/marchi/internal/search"
	syncengine "github.com/yurydemin/marchi/internal/sync"
)

// refreshInterval controls how often the scheduler re-lists accounts to
// pick up ones added, removed, deactivated, or re-scheduled since the
// last pass. Cron expressions have minute granularity anyway, so a
// 1-minute refresh can't miss a scheduling window.
const refreshInterval = time.Minute

// shutdownDrainTimeout bounds how long Stop waits for any sync already
// running in the worker pool to finish — comfortably under main.go's 30s
// force-exit watchdog, leaving room for the rest of shutdown (WAL
// checkpoint, DB close) to still happen afterward.
const shutdownDrainTimeout = 20 * time.Second

// Deps bundles the already-unlocked dependencies a scheduled sync needs:
// everything internal/sync.SyncAccount itself takes, plus the repo/manager
// needed to list accounts and decrypt passwords.
type Deps struct {
	AccountsRepo    *repo.AccountsRepo
	FoldersRepo     *repo.FoldersRepo
	EmailsRepo      *repo.EmailsRepo
	AttachmentsRepo *repo.AttachmentsRepo
	SyncLogsRepo    *repo.SyncLogsRepo
	Manager         *account.Manager
	Writer          writer.Writer
	Host            string
	Index           *search.Index // nil skips search indexing entirely
}

type scheduledEntry struct {
	entryID cron.EntryID
	spec    string
}

// Scheduler runs each active account's sync on its own cron schedule
// (a.SyncCron, falling back to cfg.Sync.DefaultSchedule), capping how many
// run concurrently at cfg.Sync.MaxConcurrentAccounts.
type Scheduler struct {
	cfg    *config.Config
	logger *zap.Logger
	deps   Deps

	cronSched *cron.Cron
	pool      *ants.Pool

	mu      sync.Mutex
	entries map[int64]scheduledEntry // account ID -> current schedule
}

// New builds a Scheduler. It does not run anything until Start is called.
func New(cfg *config.Config, logger *zap.Logger, deps Deps) (*Scheduler, error) {
	size := cfg.Sync.MaxConcurrentAccounts
	if size < 1 {
		size = 1
	}
	pool, err := ants.NewPool(size)
	if err != nil {
		return nil, fmt.Errorf("scheduler: creating worker pool: %w", err)
	}
	return &Scheduler{
		cfg:       cfg,
		logger:    logger,
		deps:      deps,
		cronSched: cron.New(),
		pool:      pool,
		entries:   make(map[int64]scheduledEntry),
	}, nil
}

// Start reconciles the initial schedule, starts the cron scheduler, and
// launches a background refresh loop that keeps reconciling it every
// refreshInterval until ctx is cancelled.
func (s *Scheduler) Start(ctx context.Context) {
	s.refresh(ctx)
	s.cronSched.Start()

	go func() {
		ticker := time.NewTicker(refreshInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.refresh(ctx)
			}
		}
	}()
}

// Stop stops the cron scheduler (waiting for any in-flight
// AddFunc-registered call to return — see robfig/cron's own Stop doc),
// then drains the worker pool so an already-running sync gets a chance to
// finish before the process closes the database out from under it.
func (s *Scheduler) Stop() {
	<-s.cronSched.Stop().Done()
	if err := s.pool.ReleaseTimeout(shutdownDrainTimeout); err != nil {
		s.logger.Warn("scheduler: worker pool did not drain within timeout", zap.Error(err))
	}
}

// refresh re-lists active accounts and reconciles the cron schedule against
// them: newly-active accounts get a cron entry, deactivated/removed ones
// lose theirs, and accounts whose cron expression changed get rescheduled.
func (s *Scheduler) refresh(ctx context.Context) {
	accounts, err := s.deps.AccountsRepo.List(ctx)
	if err != nil {
		s.logger.Error("scheduler: listing accounts failed, keeping previous schedule", zap.Error(err))
		return
	}

	wanted := make(map[int64]string, len(accounts))
	for _, a := range accounts {
		if !a.IsActive {
			continue
		}
		spec := a.SyncCron
		if spec == "" {
			spec = s.cfg.Sync.DefaultSchedule
		}
		wanted[a.ID] = spec
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for id, spec := range wanted {
		if existing, ok := s.entries[id]; ok {
			if existing.spec == spec {
				continue // unchanged
			}
			s.cronSched.Remove(existing.entryID)
		}

		accountID := id
		entryID, err := s.cronSched.AddFunc(spec, func() { s.runSync(accountID) })
		if err != nil {
			s.logger.Error("scheduler: invalid cron expression, account not scheduled",
				zap.Int64("account_id", accountID), zap.String("cron", spec), zap.Error(err))
			delete(s.entries, id)
			continue
		}
		s.entries[id] = scheduledEntry{entryID: entryID, spec: spec}
	}

	for id, entry := range s.entries {
		if _, ok := wanted[id]; !ok {
			s.cronSched.Remove(entry.entryID)
			delete(s.entries, id)
		}
	}
}

// runSync submits one account's sync to the bounded worker pool. Blocking
// here — inside cron's own per-tick goroutine, not the scheduler's public
// API — until a pool slot frees up is the concurrency limit itself
// (cfg.Sync.MaxConcurrentAccounts).
func (s *Scheduler) runSync(accountID int64) {
	if err := s.pool.Submit(func() { s.syncOne(accountID) }); err != nil {
		s.logger.Error("scheduler: submitting sync to worker pool failed",
			zap.Int64("account_id", accountID), zap.Error(err))
	}
}

func (s *Scheduler) syncOne(accountID int64) {
	ctx := context.Background()

	a, err := s.deps.AccountsRepo.GetByID(ctx, accountID)
	if err != nil {
		s.logger.Error("scheduler: account disappeared before its scheduled sync ran",
			zap.Int64("account_id", accountID), zap.Error(err))
		return
	}
	if !a.IsActive {
		return // deactivated since this tick was scheduled
	}

	password, err := s.deps.Manager.DecryptPassword(a)
	if err != nil {
		s.logger.Error("scheduler: decrypting password failed", zap.String("email", a.Email), zap.Error(err))
		return
	}

	s.logger.Info("scheduler: starting sync", zap.String("email", a.Email))
	results, syncErr := syncengine.SyncAccount(ctx, a, password, s.cfg.Storage.MaildirPath, s.deps.Host,
		s.deps.Writer, s.deps.FoldersRepo, s.deps.EmailsRepo, s.deps.AttachmentsRepo, s.deps.SyncLogsRepo, s.deps.Index)

	archived := 0
	for _, r := range results {
		archived += r.Fetched
	}
	if syncErr != nil {
		s.logger.Warn("scheduler: sync finished with errors",
			zap.String("email", a.Email), zap.Int("archived", archived), zap.Error(syncErr))
		return
	}
	s.logger.Info("scheduler: sync completed", zap.String("email", a.Email), zap.Int("archived", archived))
}
