package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"go.uber.org/zap"

	"github.com/yurydemin/marchi/internal/account"
	"github.com/yurydemin/marchi/internal/config"
	"github.com/yurydemin/marchi/internal/db"
	"github.com/yurydemin/marchi/internal/db/repo"
	"github.com/yurydemin/marchi/internal/db/writer"
	"github.com/yurydemin/marchi/internal/domain"
	"github.com/yurydemin/marchi/internal/reindex"
	"github.com/yurydemin/marchi/internal/rules"
	"github.com/yurydemin/marchi/internal/s3config"
	"github.com/yurydemin/marchi/internal/s3store"
	"github.com/yurydemin/marchi/internal/scheduler"
	"github.com/yurydemin/marchi/internal/search"
	syncengine "github.com/yurydemin/marchi/internal/sync"
)

// rulesYAMLFilename is FR-RE-05's optional rules.yaml, resolved relative
// to the data directory (alongside mailvault.db and .salt) — the same
// convention every other data-dir-rooted file in this project already
// follows, rather than adding a dedicated config field for a single
// optional path.
const rulesYAMLFilename = "rules.yaml"

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
	// s3ConfigRepo/s3UploadQueueRepo feed the Scheduler (SyncAccount
	// mirror-enqueues via them, FR-S3-03) and the S3 Settings API
	// (s3ConfigManager wraps s3ConfigRepo with the credential subkey).
	s3ConfigRepo      *repo.S3ConfigRepo
	s3UploadQueueRepo *repo.S3UploadQueueRepo
	s3ConfigManager   *s3config.Manager
	manager           *account.Manager

	// s3Uploader is the mirror upload queue's worker pool (FR-S3-06),
	// started only if s3_config exists and is Enabled at unlock time —
	// see newBackend. A settings change made afterward via the Settings
	// API (enabling S3 for the first time, or editing credentials) takes
	// effect on the next unlock/restart, not live; hot-swapping a running
	// worker pool's credentials was judged not worth the added complexity
	// against how rarely S3 settings actually change once configured.
	s3Uploader     *s3store.Uploader
	stopS3Uploader context.CancelFunc

	// indexMu guards index itself (not the index's own internal state):
	// a live reindex (FR-SR-04's admin endpoint) closes the current index
	// and swaps in a freshly rebuilt one without restarting the server,
	// so every reader of b.index has to go through currentIndex() rather
	// than a plain field read/write, which would otherwise be a data race.
	indexMu sync.RWMutex
	index   *search.Index

	scheduler *scheduler.Scheduler
	stopSched context.CancelFunc

	// stopRulesWatch cancels the rules.yaml fsnotify watcher goroutine
	// (FR-RE-05). Best-effort on shutdown, like the watcher itself: close()
	// signals it to stop but doesn't block waiting for the goroutine to
	// actually exit before closing the writer — a reload that loses this
	// race just gets a logged, harmless "writer closed" error from Do()
	// (see writer.Writer's own close-rejects-new-work behavior) rather
	// than corrupting anything.
	stopRulesWatch context.CancelFunc

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

	s3ConfigRepo := repo.NewS3ConfigRepo(sqlDB, w)
	s3ConfigMgr, err := s3config.NewManager(s3ConfigRepo, masterKey)
	if err != nil {
		_ = idx.Close()
		w.Close()
		_ = db.Close(sqlDB)
		return nil, fmt.Errorf("httpapi: initializing s3 config manager: %w", err)
	}

	b := &backend{
		sqlDB:             sqlDB,
		w:                 w,
		maildirRoot:       cfg.Storage.MaildirPath,
		indexPath:         cfg.Search.IndexPath,
		wsHub:             hub,
		accountsRepo:      accountsRepo,
		foldersRepo:       repo.NewFoldersRepo(sqlDB, w),
		emailsRepo:        repo.NewEmailsRepo(sqlDB, w),
		attachmentsRepo:   repo.NewAttachmentsRepo(sqlDB, w),
		syncLogsRepo:      repo.NewSyncLogsRepo(sqlDB, w),
		rulesRepo:         repo.NewRulesRepo(sqlDB, w),
		s3ConfigRepo:      s3ConfigRepo,
		s3UploadQueueRepo: repo.NewS3UploadQueueRepo(sqlDB, w),
		s3ConfigManager:   s3ConfigMgr,
		manager:           mgr,
		index:             idx,
	}

	sched, err := scheduler.New(cfg, logger, scheduler.Deps{
		AccountsRepo:      b.accountsRepo,
		FoldersRepo:       b.foldersRepo,
		EmailsRepo:        b.emailsRepo,
		AttachmentsRepo:   b.attachmentsRepo,
		SyncLogsRepo:      b.syncLogsRepo,
		RulesRepo:         b.rulesRepo,
		S3ConfigRepo:      b.s3ConfigRepo,
		S3UploadQueueRepo: b.s3UploadQueueRepo,
		Manager:           b.manager,
		Writer:            b.w,
		Host:              host,
		IndexFunc:         b.currentIndex,
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

	rulesCtx, stopRulesWatch := context.WithCancel(context.Background())
	go rules.WatchYAML(rulesCtx, filepath.Join(cfg.App.DataDir, rulesYAMLFilename), b.rulesRepo, logger)
	b.stopRulesWatch = stopRulesWatch

	s3UploaderCtx, stopS3Uploader := context.WithCancel(context.Background())
	b.stopS3Uploader = stopS3Uploader
	b.startS3Uploader(s3UploaderCtx, cfg, logger, masterKey)

	logger.Info("backend initialized: database opened, scheduler started")
	return b, nil
}

// startS3Uploader starts the mirror upload queue's worker pool
// (FR-S3-06) if s3_config exists and is Enabled — see the doc comment on
// backend.s3Uploader for why this only happens once, at unlock time, not
// on every Settings API change. Any failure here (no config yet, bad
// credentials, unreachable endpoint) is logged and otherwise harmless:
// mirroring simply stays off, exactly as if S3 had never been configured
// — enqueued rows just accumulate in s3_upload_queue until an uploader
// does start draining them.
func (b *backend) startS3Uploader(ctx context.Context, cfg *config.Config, logger *zap.Logger, masterKey []byte) {
	s3cfg, err := b.s3ConfigManager.Get(context.Background())
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			logger.Warn("httpapi: loading s3 config failed, mirror uploader not started", zap.Error(err))
		}
		return
	}
	if !s3cfg.Enabled {
		return
	}

	accessKey, secretKey, err := b.s3ConfigManager.DecryptCredentials(s3cfg)
	if err != nil {
		logger.Warn("httpapi: decrypting s3 credentials failed, mirror uploader not started", zap.Error(err))
		return
	}

	client, err := s3store.NewClient(s3store.Options{
		Endpoint: s3cfg.Endpoint, Region: s3cfg.Region, Bucket: s3cfg.Bucket,
		AccessKeyID: accessKey, SecretAccessKey: secretKey,
		PathStyle: s3cfg.PathStyle, TLSSkipVerify: s3cfg.TLSSkipVerify,
	})
	if err != nil {
		logger.Warn("httpapi: building s3 client failed, mirror uploader not started", zap.Error(err))
		return
	}

	uploader := s3store.NewUploader(s3store.UploaderDeps{
		Client: client, QueueRepo: b.s3UploadQueueRepo, MasterKey: masterKey, Logger: logger,
		Workers: cfg.S3.UploadWorkers,
		OnError: func(item *domain.S3UploadQueueItem, uploadErr error) {
			b.wsHub.broadcast(s3UploadErrorWSEvent(item, uploadErr))
		},
	})
	uploader.Start(ctx)
	b.s3Uploader = uploader
	logger.Info("httpapi: s3 mirror uploader started", zap.String("bucket", s3cfg.Bucket))
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
	b.stopRulesWatch()
	if b.s3Uploader != nil {
		b.stopS3Uploader()
		b.s3Uploader.Stop()
	}
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
