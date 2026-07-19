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

	"github.com/google/uuid"
	"github.com/panjf2000/ants/v2"
	"github.com/robfig/cron/v3"
	"go.uber.org/zap"

	"github.com/yurydemin/marchi/internal/account"
	"github.com/yurydemin/marchi/internal/config"
	"github.com/yurydemin/marchi/internal/db/repo"
	"github.com/yurydemin/marchi/internal/db/writer"
	"github.com/yurydemin/marchi/internal/domain"
	"github.com/yurydemin/marchi/internal/retention"
	"github.com/yurydemin/marchi/internal/search"
	syncengine "github.com/yurydemin/marchi/internal/sync"
)

// defaultRetentionCron is FR-RE-04's "ежедневно в 03:00" — used whenever
// Deps.RetentionCron is left empty.
const defaultRetentionCron = "0 3 * * *"

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
	// RulesRepo, if nil, means no Rule Engine dispatch: every message
	// defaults to archive, matching Phase 1/2's unconditional behavior.
	RulesRepo *repo.RulesRepo
	// S3ConfigRepo/S3UploadQueueRepo, if both non-nil, let SyncAccount
	// mirror newly-archived emails to S3 (FR-S3-03) when s3_config is
	// Enabled — see SyncAccount's own doc comment for the nil-means-off
	// convention this follows.
	S3ConfigRepo      *repo.S3ConfigRepo
	S3UploadQueueRepo *repo.S3UploadQueueRepo
	// RetentionRunner, if nil, means the retention cron never runs at
	// all — the same nil-means-off convention as RulesRepo/S3ConfigRepo
	// above. RetentionCron overrides defaultRetentionCron if set.
	RetentionRunner *retention.Runner
	RetentionCron   string
	Manager         *account.Manager
	Writer          writer.Writer
	Host            string
	// IndexFunc returns the search index to use for the sync about to run,
	// resolved fresh each time rather than captured once — so a caller
	// that swaps its index at runtime (a live reindex, FR-SR-04) is picked
	// up on the next scheduled sync without recreating the Scheduler. A
	// nil IndexFunc, or one returning nil, skips search indexing entirely.
	IndexFunc func() *search.Index
	// ProgressFunc, if set, is called with a job id (fresh per sync run)
	// and every syncengine.Progress update for a sync this Scheduler runs
	// (FR-SE-07) — the same mechanism internal/httpapi uses for its own
	// WebSocket broadcasts, so a connected client sees live progress the
	// same way regardless of whether the sync was scheduled or manually
	// triggered via TriggerSync.
	ProgressFunc func(jobID string, a *domain.Account, p syncengine.Progress)
	// CompletedFunc, if set, is called once a sync run this Scheduler ran
	// has finished (successfully or not), with the same job id
	// ProgressFunc saw. Separate from ProgressFunc because "the run is
	// over" isn't itself a Progress update — there's no next message to
	// report one for.
	CompletedFunc func(jobID string, a *domain.Account, archived int, err error)
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

	if s.deps.RetentionRunner != nil {
		spec := s.deps.RetentionCron
		if spec == "" {
			spec = defaultRetentionCron
		}
		if _, err := s.cronSched.AddFunc(spec, func() { s.runRetention(ctx) }); err != nil {
			s.logger.Error("scheduler: invalid retention cron expression, retention will not run automatically",
				zap.String("cron", spec), zap.Error(err))
		}
	}

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

// runSync submits one account's scheduled sync to the bounded worker
// pool, generating its own job id. Blocking here — inside cron's own
// per-tick goroutine, not the scheduler's public API — until a pool slot
// frees up is the concurrency limit itself (cfg.Sync.MaxConcurrentAccounts).
func (s *Scheduler) runSync(accountID int64) {
	jobID := uuid.NewString()
	if err := s.pool.Submit(func() { s.syncOne(accountID, jobID) }); err != nil {
		s.logger.Error("scheduler: submitting sync to worker pool failed",
			zap.Int64("account_id", accountID), zap.Error(err))
	}
}

// TriggerSync submits an immediate, one-off sync for accountID onto the
// same bounded worker pool scheduled ticks use (FR-SE-06: "ручной запуск
// через Web UI или CLI") — so a manually-triggered sync shares both the
// concurrency cap and, critically, the graceful-shutdown drain (Stop)
// every other sync this Scheduler runs already gets, rather than being a
// bare detached goroutine main.go's shutdown watchdog doesn't know about.
// Returns a job id the caller can hand back to an HTTP client immediately
// (before the pool necessarily gets around to running it) to correlate
// against ProgressFunc/WebSocket events.
func (s *Scheduler) TriggerSync(accountID int64) (string, error) {
	jobID := uuid.NewString()
	if err := s.pool.Submit(func() { s.syncOne(accountID, jobID) }); err != nil {
		return "", fmt.Errorf("scheduler: submitting manual sync to worker pool failed: %w", err)
	}
	return jobID, nil
}

func (s *Scheduler) syncOne(accountID int64, jobID string) {
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

	var idx *search.Index
	if s.deps.IndexFunc != nil {
		idx = s.deps.IndexFunc()
	}

	var onProgress syncengine.ProgressFunc
	if s.deps.ProgressFunc != nil {
		onProgress = func(p syncengine.Progress) { s.deps.ProgressFunc(jobID, a, p) }
	}

	s.logger.Info("scheduler: starting sync", zap.String("email", a.Email))
	results, syncErr := syncengine.SyncAccount(ctx, a, password, s.cfg.Storage.MaildirPath, s.deps.Host,
		s.deps.Writer, s.deps.FoldersRepo, s.deps.EmailsRepo, s.deps.AttachmentsRepo, s.deps.SyncLogsRepo,
		s.deps.RulesRepo, idx, s.deps.S3ConfigRepo, s.deps.S3UploadQueueRepo, onProgress)

	archived := 0
	for _, r := range results {
		archived += r.Fetched
	}
	if s.deps.CompletedFunc != nil {
		s.deps.CompletedFunc(jobID, a, archived, syncErr)
	}
	if syncErr != nil {
		s.logger.Warn("scheduler: sync finished with errors",
			zap.String("email", a.Email), zap.Int("archived", archived), zap.Error(syncErr))
		return
	}
	s.logger.Info("scheduler: sync completed", zap.String("email", a.Email), zap.Int("archived", archived))
}

// TriggerRetention submits an immediate, one-off retention pass onto the
// same bounded worker pool sync uses (mirroring TriggerSync) — a manual
// "run retention now" action (Settings UI, `mailvault retention run`
// against a live server) shares the same concurrency cap and
// graceful-shutdown drain as everything else this Scheduler runs.
func (s *Scheduler) TriggerRetention() error {
	if s.deps.RetentionRunner == nil {
		return fmt.Errorf("scheduler: retention is not configured")
	}
	if err := s.pool.Submit(func() { s.runRetention(context.Background()) }); err != nil {
		return fmt.Errorf("scheduler: submitting retention run to worker pool failed: %w", err)
	}
	return nil
}

// runRetention runs one retention pass and logs the outcome. Errors are
// logged, not propagated — cron's own AddFunc has nowhere to send an
// error, and a failed pass simply gets retried on the next scheduled tick
// (or the next manual trigger).
func (s *Scheduler) runRetention(ctx context.Context) {
	s.logger.Info("scheduler: starting retention run")
	stats, err := s.deps.RetentionRunner.Run(ctx)
	if err != nil {
		s.logger.Error("scheduler: retention run failed", zap.Error(err))
		return
	}
	s.logger.Info("scheduler: retention run completed",
		zap.Int("moved_to_s3_only", stats.MovedToS3Only),
		zap.Int("deleted_direct", stats.DeletedDirect),
		zap.Int("deleted_from_s3", stats.DeletedFromS3),
		zap.Int("errors", stats.Errors))
}
