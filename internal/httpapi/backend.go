package httpapi

import (
	"context"
	"database/sql"
	"fmt"
	"os"

	"go.uber.org/zap"

	"github.com/yurydemin/marchi/internal/account"
	"github.com/yurydemin/marchi/internal/config"
	"github.com/yurydemin/marchi/internal/db"
	"github.com/yurydemin/marchi/internal/db/repo"
	"github.com/yurydemin/marchi/internal/db/writer"
	"github.com/yurydemin/marchi/internal/scheduler"
	"github.com/yurydemin/marchi/internal/search"
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

	accountsRepo    *repo.AccountsRepo
	foldersRepo     *repo.FoldersRepo
	emailsRepo      *repo.EmailsRepo
	attachmentsRepo *repo.AttachmentsRepo
	syncLogsRepo    *repo.SyncLogsRepo
	manager         *account.Manager
	index           *search.Index

	scheduler *scheduler.Scheduler
	stopSched context.CancelFunc
}

// newBackend opens the database, builds every repo, and starts the
// Scheduler (FR-SE-06) — the same wiring cmd_sync.go's CLI command does,
// just done once for the long-running server process instead of once per
// invocation.
func newBackend(cfg *config.Config, logger *zap.Logger, masterKey []byte) (*backend, error) {
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
		accountsRepo:    accountsRepo,
		foldersRepo:     repo.NewFoldersRepo(sqlDB, w),
		emailsRepo:      repo.NewEmailsRepo(sqlDB, w),
		attachmentsRepo: repo.NewAttachmentsRepo(sqlDB, w),
		syncLogsRepo:    repo.NewSyncLogsRepo(sqlDB, w),
		manager:         mgr,
		index:           idx,
	}

	sched, err := scheduler.New(cfg, logger, scheduler.Deps{
		AccountsRepo:    b.accountsRepo,
		FoldersRepo:     b.foldersRepo,
		EmailsRepo:      b.emailsRepo,
		AttachmentsRepo: b.attachmentsRepo,
		SyncLogsRepo:    b.syncLogsRepo,
		Manager:         b.manager,
		Writer:          b.w,
		Host:            host,
		Index:           b.index,
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

// close stops the scheduler, the Single Writer, the search index, and the
// database, in that order — each step waits for the previous one's work
// to be safely done before tearing down what it depended on.
func (b *backend) close(logger *zap.Logger) {
	b.stopSched()
	b.scheduler.Stop()
	if err := b.w.Close(); err != nil {
		logger.Warn("closing writer failed", zap.Error(err))
	}
	if err := b.index.Close(); err != nil {
		logger.Warn("closing search index failed", zap.Error(err))
	}
	if err := db.Close(b.sqlDB); err != nil {
		logger.Warn("closing database failed", zap.Error(err))
	}
}
