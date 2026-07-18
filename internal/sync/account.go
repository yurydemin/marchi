package sync

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/yurydemin/marchi/internal/db/repo"
	"github.com/yurydemin/marchi/internal/db/writer"
	"github.com/yurydemin/marchi/internal/domain"
	"github.com/yurydemin/marchi/internal/imapclient"
	"github.com/yurydemin/marchi/internal/maildir"
	"github.com/yurydemin/marchi/internal/search"
)

// syncLogWriteTimeout bounds the sync_logs Start/Finish writes, which
// deliberately run on their own context rather than the caller's
// shutdown-aware one (see SyncAccount) — bounded so a genuinely stuck
// writer can't hang shutdown forever, but long enough that it's never the
// limiting factor in practice.
const syncLogWriteTimeout = 5 * time.Second

// FolderResult summarizes one folder's sync-then-fetch pass.
type FolderResult struct {
	Folder  *domain.Folder
	Fetched int
}

// SyncAccount connects to a using the already-decrypted password, syncs its
// folder list (FR-SE-01), then fetches new messages for every folder into
// maildirRoot (config.yaml's storage.maildir_path). host names the Maildir
// filename's hostname component (see maildir.NewWriter). idx, if non-nil,
// gets each newly-archived email indexed for search (FR-SR-01/02) on a
// best-effort basis — see archiveOne's doc comment for why that can't be
// stronger than best-effort. onProgress, if non-nil, is called after every
// message archived or failed (FR-SE-07) — see FetchNewMessages.
//
// The whole run is wrapped in a sync_logs row (FR-SE-06/07): Start is
// called before anything else, and Finish always runs via defer — even if
// connecting fails outright — so every invocation leaves a record, not just
// the ones that got far enough to touch a folder. A failure to write the
// log itself is logged nowhere further up and never masks the real sync
// result; it's a best-effort record, not a dependency the sync's success
// hinges on.
//
// Start/Finish deliberately run on their own short-lived context instead
// of ctx: ctx is the caller's shutdown-aware context (SIGINT/SIGTERM
// cancels it, NFR-RL-05), and a run that gets cancelled specifically needs
// its sync_logs row to still get written — recording "cancelled" is the
// whole point, and that write must not itself be vulnerable to the same
// cancellation it's trying to record.
//
// A folder-level fetch error stops that folder (see FetchNewMessages'
// own doc comment on why) but does not abort the remaining folders — one
// folder having trouble shouldn't block archiving the rest of the account.
// The first error encountered, if any, is returned alongside every result
// gathered up to that point.
func SyncAccount(
	ctx context.Context,
	a *domain.Account,
	password string,
	maildirRoot, host string,
	w writer.Writer,
	foldersRepo *repo.FoldersRepo,
	emailsRepo *repo.EmailsRepo,
	attachmentsRepo *repo.AttachmentsRepo,
	syncLogsRepo *repo.SyncLogsRepo,
	rulesRepo *repo.RulesRepo, // nil skips Rule Engine dispatch entirely — every message defaults to archive (FR-RE-03)
	idx *search.Index, // nil skips search indexing entirely — see FetchNewMessages/archiveOne
	// s3ConfigRepo/s3QueueRepo, if both non-nil, are checked once per
	// run (like activeRules below) — S3 mirroring only actually happens
	// if s3_config exists and is Enabled (FR-S3-03). Either being nil
	// disables mirroring outright, no query issued — the same nil-means-
	// off convention every other optional dependency here already uses.
	s3ConfigRepo *repo.S3ConfigRepo,
	s3QueueRepo *repo.S3UploadQueueRepo,
	onProgress ProgressFunc, // nil skips progress reporting entirely (FR-SE-07)
) ([]FolderResult, error) {
	startCtx, cancelStart := context.WithTimeout(context.Background(), syncLogWriteTimeout)
	logID, logErr := syncLogsRepo.Start(startCtx, a.ID)
	cancelStart()

	var total FetchStats
	var syncErr error
	defer func() {
		if logErr != nil {
			return // Start itself failed — nothing to Finish
		}
		status := domain.SyncLogCompleted
		errMsg := ""
		switch {
		case syncErr == nil:
			// status/errMsg already correct
		case errors.Is(syncErr, context.Canceled):
			// A graceful shutdown (NFR-RL-05) is a deliberate stop, not a
			// failure — sync_logs should say so distinctly.
			status = domain.SyncLogCancelled
			errMsg = "sync cancelled by shutdown signal"
		default:
			status = domain.SyncLogFailed
			errMsg = syncErr.Error()
		}

		finishCtx, cancelFinish := context.WithTimeout(context.Background(), syncLogWriteTimeout)
		defer cancelFinish()
		_ = syncLogsRepo.Finish(finishCtx, logID, &domain.SyncLog{
			EmailsProcessed: total.Processed,
			EmailsArchived:  total.Archived,
			EmailsSkipped:   total.Skipped,
			BytesDownloaded: total.Bytes,
			Errors:          total.Errors,
			Status:          status,
			ErrorMsg:        errMsg,
		})
	}()

	c, err := imapclient.Connect(ctx, imapclient.ConnectOptions{
		Host:     a.IMAPHost,
		Port:     a.IMAPPort,
		TLS:      a.IMAPTLS,
		Username: a.IMAPUsername,
		Password: password,
	})
	if err != nil {
		syncErr = err
		return nil, err
	}
	defer c.Logout()

	folders, err := SyncFolders(ctx, c, a.ID, foldersRepo)
	if err != nil {
		syncErr = err
		return nil, err
	}

	// Loaded once per account sync, not per folder — every folder's
	// FetchNewMessages call evaluates against the same rule set.
	var activeRules []*domain.Rule
	if rulesRepo != nil {
		activeRules, err = rulesRepo.ListActive(ctx)
		if err != nil {
			syncErr = fmt.Errorf("sync: loading active rules: %w", err)
			return nil, syncErr
		}
	}

	// Resolved once per account sync too: whether S3 mirroring is
	// currently on. sql.ErrNoRows (S3 never configured) is the ordinary
	// zero-config state, not a failure — it just means mirroring stays
	// off, same as if s3ConfigRepo/s3QueueRepo were nil to begin with.
	var s3Queue *repo.S3UploadQueueRepo
	if s3ConfigRepo != nil && s3QueueRepo != nil {
		s3cfg, err := s3ConfigRepo.Get(ctx)
		if err == nil && s3cfg.Enabled {
			s3Queue = s3QueueRepo
		} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
			syncErr = fmt.Errorf("sync: loading s3 config: %w", err)
			return nil, syncErr
		}
	}

	results := make([]FolderResult, 0, len(folders))
	var firstErr error
	for _, folder := range folders {
		if firstErr != nil {
			results = append(results, FolderResult{Folder: folder})
			continue
		}
		// Graceful shutdown (NFR-RL-05): don't start fetching another
		// folder once a SIGINT/SIGTERM has been requested. The folder
		// whose fetch is already in flight — if any — finishes or rolls
		// back cleanly on its own (FetchNewMessages' own ctx check).
		if ctx.Err() != nil {
			firstErr = ctx.Err()
			results = append(results, FolderResult{Folder: folder})
			continue
		}

		dir := maildir.FolderDir(maildirRoot, a.ID, maildir.SafeFolderName(folder.FolderName))
		layout, layoutErr := maildir.NewLayout(dir)
		if layoutErr != nil {
			firstErr = fmt.Errorf("sync: preparing maildir for %q: %w", folder.FolderName, layoutErr)
			results = append(results, FolderResult{Folder: folder})
			continue
		}
		mw := maildir.NewWriter(layout, host)

		stats, fetchErr := FetchNewMessages(ctx, c, a.ID, folder, mw, w, emailsRepo, foldersRepo, attachmentsRepo, idx, s3Queue, activeRules, onProgress)
		total.Processed += stats.Processed
		total.Archived += stats.Archived
		total.Skipped += stats.Skipped
		total.Bytes += stats.Bytes
		// RuleActionErrors folds into the same visible Errors count sync_logs
		// already surfaces — both mean "something needs a look", even though
		// only the archival kind (not a post-archive STORE/EXPUNGE failure)
		// ever sets firstErr/syncErr.
		total.Errors += stats.Errors + stats.RuleActionErrors
		total.IndexErrors += stats.IndexErrors
		results = append(results, FolderResult{Folder: folder, Fetched: stats.Archived})
		if fetchErr != nil {
			firstErr = fmt.Errorf("sync: fetching %q: %w", folder.FolderName, fetchErr)
		}
	}
	syncErr = firstErr
	return results, firstErr
}
