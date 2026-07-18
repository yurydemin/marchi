package httpapi

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sync"

	"go.uber.org/zap"

	"github.com/yurydemin/marchi/internal/account"
	"github.com/yurydemin/marchi/internal/config"
	"github.com/yurydemin/marchi/internal/db"
	"github.com/yurydemin/marchi/internal/db/repo"
	"github.com/yurydemin/marchi/internal/db/writer"
	"github.com/yurydemin/marchi/internal/domain"
	"github.com/yurydemin/marchi/internal/reindex"
	"github.com/yurydemin/marchi/internal/scheduler"
	"github.com/yurydemin/marchi/internal/search"
	syncengine "github.com/yurydemin/marchi/internal/sync"
)

// backend holds everything that requires the vault to be unlocked: the
// SQLite connection, Single Writer, repos, Account Manager, search index,
// and the Scheduler built on top of them. It's constructed exactly once,
// the moment the vault transitions from locked to unlocked — see
// vaultState.unlock — and lives for the rest of the process.
type backend struct {
	sqlDB       *sql.DB
	w           writer.Writer
	maildirRoot string
	indexPath   string
	wsHub       *wsHub

	accountsRepo    *repo.AccountsRepo
	foldersRepo     *repo.FoldersRepo
	emailsRepo      *repo.EmailsRepo
	attachmentsRepo *repo.AttachmentsRepo
	syncLogsRepo    *repo.SyncLogsRepo
	rulesRepo       *repo.RulesRepo
	manager         *account.Manager

	// indexMu guards index itself (not the index's own internal state):
	// a live reindex (FR-SR-04's admin endpoint) closes the current index
	// and swaps in a freshly rebuilt one without restarting the server,
	// so every reader of b.index has to go through currentIndex() rather
	// than a plain field read/write, which would otherwise be a data race.
	indexMu sync.RWMutex
	index   *search.Index

	scheduler *scheduler.Scheduler
	stopSched context.CancelFunc

	// bgJobs tracks detached background work that isn't already covered
	// by the Scheduler's own worker pool (sync goes through
	// scheduler.TriggerSync, which shares the pool's drain-on-Stop; an
	// async reindex has no such pool, so it gets its own tracking here) —
	// close() waits on this before tearing down what that work depends on.
	bgJobs sync.WaitGroup
}

// newBackend opens the database, builds every repo, and starts the
// Scheduler (FR-SE-06) — the same wiring cmd_sync.go's CLI command does,
// just done once for the long-running server process instead of once per
// invocation. hub is where sync/reindex progress (FR-SE-07/FR-SR-04) gets
// broadcast to any connected /ws client.
func newBackend(cfg *config.Config, logger *zap.Logger, masterKey []byte, hub *wsHub) (*backend, error) {
	sqlDB, err := db.Open(cfg.Database.SQLite.Path)
	if err != nil {
		return nil, fmt.Errorf("httpapi: opening database: %w", err)
	}

	w := writer.New(sqlDB)
	accountsRepo := repo.NewAccountsRepo(sqlDB, w)

	mgr, err := account.NewManager(accountsRepo, masterKey)
	if err != nil {
		w.Close()
		_ = db.Close(sqlDB)
		return nil, fmt.Errorf("httpapi: initializing account manager: %w", err)
	}

	host, err := os.Hostname()
	if err != nil {
		host = "localhost"
	}

	idx, err := search.Open(cfg.Search.IndexPath)
	if err != nil {
		w.Close()
		_ = db.Close(sqlDB)
		return nil, fmt.Errorf("httpapi: opening search index: %w", err)
	}

	b := &backend{
		sqlDB:           sqlDB,
		w:               w,
		maildirRoot:     cfg.Storage.MaildirPath,
		indexPath:       cfg.Search.IndexPath,
		wsHub:           hub,
		accountsRepo:    accountsRepo,
		foldersRepo:     repo.NewFoldersRepo(sqlDB, w),
		emailsRepo:      repo.NewEmailsRepo(sqlDB, w),
		attachmentsRepo: repo.NewAttachmentsRepo(sqlDB, w),
		syncLogsRepo:    repo.NewSyncLogsRepo(sqlDB, w),
		rulesRepo:       repo.NewRulesRepo(sqlDB, w),
		manager:         mgr,
		index:           idx,
	}

	sched, err := scheduler.New(cfg, logger, scheduler.Deps{
		AccountsRepo:    b.accountsRepo,
		FoldersRepo:     b.foldersRepo,
		EmailsRepo:      b.emailsRepo,
		AttachmentsRepo: b.attachmentsRepo,
		SyncLogsRepo:    b.syncLogsRepo,
		RulesRepo:       b.rulesRepo,
		Manager:         b.manager,
		Writer:          b.w,
		Host:            host,
		IndexFunc:       b.currentIndex,
		ProgressFunc: func(jobID string, a *domain.Account, p syncengine.Progress) {
			hub.broadcast(syncWSEvent(jobID, a, p, false, ""))
		},
		CompletedFunc: func(jobID string, a *domain.Account, archived int, syncErr error) {
			errMsg := ""
			if syncErr != nil {
				errMsg = syncErr.Error()
			}
			hub.broadcast(syncWSEvent(jobID, a, syncengine.Progress{Archived: archived}, true, errMsg))
		},
	})
	if err != nil {
		_ = idx.Close()
		w.Close()
		_ = db.Close(sqlDB)
		return nil, fmt.Errorf("httpapi: initializing scheduler: %w", err)
	}
	schedCtx, stopSched := context.WithCancel(context.Background())
	sched.Start(schedCtx)
	b.scheduler = sched
	b.stopSched = stopSched

	logger.Info("backend initialized: database opened, scheduler started")
	return b, nil
}

// currentIndex returns the search index currently in use. Every consumer
// (search/emails/accounts HTTP handlers, the Scheduler via IndexFunc)
// calls this instead of holding onto a *search.Index of their own, so a
// live reindex's swap is visible to all of them immediately.
func (b *backend) currentIndex() *search.Index {
	b.indexMu.RLock()
	defer b.indexMu.RUnlock()
	return b.index
}

// runReindex implements the admin-triggered live reindex (FR-SR-04): close
// the current index, wipe and rebuild it (internal/reindex.Run), and swap
// in the result — all under indexMu's write lock, so no concurrent
// currentIndex() caller ever observes a half-closed or half-built index.
//
// This does not pause the Scheduler. A sync already in flight when a
// reindex starts fetched its own *search.Index via IndexFunc before the
// lock was taken, and keeps using that (about-to-be-closed) reference for
// the rest of its run. That write isn't silently lost, though: indexing
// has been best-effort ever since archiveOne first started doing it (see
// internal/sync/fetch.go) — a write against a closed index fails cleanly
// (internal/search.Index bounds every write with a timeout precisely so a
// broken index can't hang the caller) and is counted as an ordinary
// IndexError, recoverable by the reindex that's already running. Given
// how infrequent an admin-triggered reindex is expected to be, that
// narrow window is an acceptable trade against the complexity of pausing
// and resuming the Scheduler mid-flight.
func (b *backend) runReindex(ctx context.Context, onProgress reindex.ProgressFunc) (reindex.Stats, error) {
	b.indexMu.Lock()
	defer b.indexMu.Unlock()

	if err := b.index.Close(); err != nil {
		return reindex.Stats{}, fmt.Errorf("httpapi: closing current index before reindex: %w", err)
	}
	newIdx, stats, err := reindex.Run(ctx, b.emailsRepo, b.indexPath, onProgress)
	if newIdx != nil {
		b.index = newIdx
	}
	return stats, err
}

// runReindexAsync runs runReindex in a tracked background goroutine
// (bgJobs, so graceful shutdown waits for it — see close()) and
// broadcasts its progress and completion over jobID, mirroring how a
// manually-triggered sync reports through the Scheduler's ProgressFunc/
// CompletedFunc.
func (b *backend) runReindexAsync(jobID string) {
	b.bgJobs.Add(1)
	go func() {
		defer b.bgJobs.Done()
		onProgress := func(s reindex.Stats) {
			b.wsHub.broadcast(reindexWSEvent(jobID, s, false, ""))
		}
		stats, err := b.runReindex(context.Background(), onProgress)
		errMsg := ""
		if err != nil {
			errMsg = err.Error()
		}
		b.wsHub.broadcast(reindexWSEvent(jobID, stats, true, errMsg))
	}()
}

// close stops the scheduler, waits for any detached background job
// (bgJobs — an in-flight async reindex), then closes the Single Writer,
// the search index, and the database, in that order — each step waits
// for the previous one's work to be safely done before tearing down what
// it depended on.
func (b *backend) close(logger *zap.Logger) {
	b.stopSched()
	b.scheduler.Stop()
	b.bgJobs.Wait()
	if err := b.w.Close(); err != nil {
		logger.Warn("closing writer failed", zap.Error(err))
	}
	if err := b.currentIndex().Close(); err != nil {
		logger.Warn("closing search index failed", zap.Error(err))
	}
	if err := db.Close(b.sqlDB); err != nil {
		logger.Warn("closing database failed", zap.Error(err))
	}
}
