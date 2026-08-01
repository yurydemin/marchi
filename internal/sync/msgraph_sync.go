package sync

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/emersion/go-imap"

	"github.com/yurydemin/marchi/internal/db/repo"
	"github.com/yurydemin/marchi/internal/db/writer"
	"github.com/yurydemin/marchi/internal/domain"
	"github.com/yurydemin/marchi/internal/maildir"
	"github.com/yurydemin/marchi/internal/mimeparse"
	"github.com/yurydemin/marchi/internal/msgraph"
	"github.com/yurydemin/marchi/internal/rules"
	"github.com/yurydemin/marchi/internal/search"
)

// msgraphUIDValidity is the constant UIDVALIDITY every ConnectorMSGraph
// folder is upserted with — Graph has no UIDVALIDITY concept at all;
// this only exists because folders.uidvalidity is part of the schema
// every connector shares. Analogous to internal/importer's own constant
// and gmailUIDValidity in msgraph_sync.go's sibling file.
const msgraphUIDValidity = 1

// msgraphFolderRef pairs a Graph folder's opaque id with the flattened
// Marchi folder name it's stored under.
type msgraphFolderRef struct {
	graphID string
	name    string // "Parent/Child" for nested folders, matching how IMAP folder names already flatten a server's hierarchy into one string
}

// SyncAccountMSGraph is SyncAccount's counterpart for a ConnectorMSGraph
// account: it fetches mail through the Microsoft Graph REST API
// (internal/msgraph) instead of IMAP, running the same Rule Engine and
// Single-Writer ArchiveOne every IMAP-synced message goes through.
//
// Folders, unlike ConnectorGmailAPI's single synthetic "All Mail"
// folder, map naturally: Microsoft Graph exposes real hierarchical mail
// folders (Inbox, Sent Items, Drafts, Deleted Items, custom folders,
// ...), much closer to IMAP than to Gmail's label model. Every folder
// Graph returns is synced — including Deleted Items and Junk Email, with
// no special-case exclusion — matching how the IMAP connector's own
// SyncFolders doesn't skip any special-use folder either (only ones
// marked \Noselect). Nested folders are walked recursively and flattened
// into "Parent/Child" names, the same convention IMAP folder names
// already use.
//
// Incremental sync: unlike Gmail's mailbox-wide history cursor (which
// needs a getProfile-before-listing dance to stay race-free — see
// internal/sync's gmailapi_sync.go), Microsoft Graph's delta query is
// scoped per folder and needs no such choreography: calling it with no
// prior link for a never-synced folder simply returns every current
// message, paginated, ending in a deltaLink that already correctly
// covers everything from that point forward. Each folder's cursor
// (folders.msgraph_delta_link) is persisted only once that folder's
// batch completes without error — the same "advance the cursor only
// after a clean batch" resumability contract Gmail's connector uses, for
// the same reason: a cursor-based mechanism can't be advanced
// message-by-message the way IMAP's last_uid watermark can, so a
// message left over from a failed batch would otherwise never be seen
// again through the delta query.
func SyncAccountMSGraph(
	ctx context.Context,
	a *domain.Account,
	client *msgraph.Client,
	maildirRoot, host string,
	w writer.Writer,
	foldersRepo *repo.FoldersRepo,
	emailsRepo *repo.EmailsRepo,
	attachmentsRepo *repo.AttachmentsRepo,
	syncLogsRepo *repo.SyncLogsRepo,
	rulesRepo *repo.RulesRepo, // nil skips Rule Engine dispatch entirely — every message defaults to archive (FR-RE-03)
	idx *search.Index, // nil skips search indexing entirely — see ArchiveOne
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
		case errors.Is(syncErr, context.Canceled):
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

	graphFolders, err := listMSGraphFoldersRecursive(ctx, client)
	if err != nil {
		syncErr = fmt.Errorf("msgraph sync: listing mail folders: %w", err)
		return nil, syncErr
	}

	var activeRules []*domain.Rule
	if rulesRepo != nil {
		activeRules, err = rulesRepo.ListActive(ctx)
		if err != nil {
			syncErr = fmt.Errorf("msgraph sync: loading active rules: %w", err)
			return nil, syncErr
		}
	}

	var s3Queue *repo.S3UploadQueueRepo
	if s3ConfigRepo != nil && s3QueueRepo != nil {
		s3cfg, err := s3ConfigRepo.Get(ctx)
		if err == nil && s3cfg.Enabled {
			s3Queue = s3QueueRepo
		} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
			syncErr = fmt.Errorf("msgraph sync: loading s3 config: %w", err)
			return nil, syncErr
		}
	}

	results := make([]FolderResult, 0, len(graphFolders))
	ruleMatches := make(map[int64]int)
	var firstErr error
	for _, gf := range graphFolders {
		if firstErr != nil {
			results = append(results, FolderResult{})
			continue
		}
		if ctx.Err() != nil {
			firstErr = ctx.Err()
			results = append(results, FolderResult{})
			continue
		}

		folder, err := foldersRepo.UpsertFolder(ctx, a.ID, gf.name, msgraphUIDValidity)
		if err != nil {
			firstErr = fmt.Errorf("msgraph sync: preparing folder %q: %w", gf.name, err)
			results = append(results, FolderResult{})
			continue
		}

		dir := maildir.FolderDir(maildirRoot, a.ID, maildir.SafeFolderName(folder.FolderName))
		layout, layoutErr := maildir.NewLayout(dir)
		if layoutErr != nil {
			firstErr = fmt.Errorf("msgraph sync: preparing maildir for %q: %w", folder.FolderName, layoutErr)
			results = append(results, FolderResult{Folder: folder})
			continue
		}
		mw := maildir.NewWriter(layout, host)

		stats, folderErr := syncOneMSGraphFolder(ctx, client, a.ID, gf.graphID, folder, mw, w,
			foldersRepo, emailsRepo, attachmentsRepo, idx, s3Queue, activeRules, onProgress)
		total.Processed += stats.Processed
		total.Archived += stats.Archived
		total.Skipped += stats.Skipped
		total.Duplicates += stats.Duplicates
		total.Bytes += stats.Bytes
		total.Errors += stats.Errors + stats.RuleActionErrors
		total.IndexErrors += stats.IndexErrors
		// Only folded in on this specific folder's own success: a failed
		// folder's deltaLink is left unpersisted (see
		// syncOneMSGraphFolder), so a retry re-lists that folder's whole
		// batch again — including messages already matched before the
		// point of failure — and would double-count them here if their
		// match had already been recorded on this attempt. A folder that
		// succeeded, by contrast, has already advanced its own cursor and
		// will never be re-listed, so recording its matches now is safe
		// regardless of whether a later folder in the same run fails.
		if folderErr == nil {
			for ruleID, n := range stats.RuleMatches {
				ruleMatches[ruleID] += n
			}
		}
		results = append(results, FolderResult{Folder: folder, Fetched: stats.Archived})
		if folderErr != nil {
			firstErr = fmt.Errorf("msgraph sync: syncing folder %q: %w", folder.FolderName, folderErr)
		}
	}

	syncErr = firstErr

	// Best-effort, same convention as search indexing/the audit log/the
	// IMAP and Gmail connectors' identical calls — see
	// internal/sync/account.go's version for the full reasoning.
	if rulesRepo != nil {
		_ = rulesRepo.RecordMatches(ctx, ruleMatches)
	}

	return results, firstErr
}

// syncOneMSGraphFolder drives one folder's delta query to completion
// (following @odata.nextLink across pages) and archives every
// non-removed message it reports, then persists the resulting deltaLink
// — but only if the whole folder was processed without error, per
// SyncAccountMSGraph's resumability contract.
func syncOneMSGraphFolder(
	ctx context.Context,
	client *msgraph.Client,
	accountID int64,
	graphFolderID string,
	folder *domain.Folder,
	mw *maildir.Writer,
	w writer.Writer,
	foldersRepo *repo.FoldersRepo,
	emailsRepo *repo.EmailsRepo,
	attachmentsRepo *repo.AttachmentsRepo,
	idx *search.Index,
	s3Queue *repo.S3UploadQueueRepo,
	activeRules []*domain.Rule,
	onProgress ProgressFunc,
) (FetchStats, error) {
	var stats FetchStats
	nextUID := folder.LastUID + 1

	link := folder.MSGraphDeltaLink
	var latestDeltaLink string
	for {
		if ctx.Err() != nil {
			return stats, ctx.Err()
		}
		page, err := client.DeltaMessages(ctx, graphFolderID, link)
		if err != nil {
			return stats, fmt.Errorf("delta query: %w", err)
		}

		for _, stub := range page.Value {
			if stub.Removed != nil {
				continue // source deletion — not propagated to the archive, see SyncAccountMSGraph's doc comment
			}
			if ctx.Err() != nil {
				return stats, ctx.Err()
			}
			stats.Processed++

			raw, fetchErr := client.GetMessageRaw(ctx, stub.ID)
			if fetchErr != nil {
				stats.Errors++
				onProgress.report(Progress{
					AccountID: accountID, FolderName: folder.FolderName, CurrentUID: nextUID,
					Processed: stats.Processed, Archived: stats.Archived, Errors: stats.Errors,
				})
				return stats, fmt.Errorf("fetching message %s: %w", stub.ID, fetchErr)
			}

			md := mimeparse.Parse(raw)
			attachments := mimeparse.ParseAttachments(raw)

			action := domain.ActionArchive
			if matched := rules.FirstMatch(activeRules, candidateFrom(md, attachments, raw, folder.FolderName, accountID)); matched != nil {
				action = matched.Action
				stats.addRuleMatch(matched.ID)
			}
			if action == domain.ActionSkip {
				stats.Skipped++
				onProgress.report(Progress{
					AccountID: accountID, FolderName: folder.FolderName, CurrentUID: nextUID,
					Processed: stats.Processed, Archived: stats.Archived, Errors: stats.Errors,
				})
				continue
			}

			if md.MessageID != "" {
				dup, dupErr := emailsRepo.ExistsByAccountMessageID(ctx, accountID, md.MessageID)
				if dupErr != nil {
					stats.Errors++
					onProgress.report(Progress{
						AccountID: accountID, FolderName: folder.FolderName, CurrentUID: nextUID,
						Processed: stats.Processed, Archived: stats.Archived, Errors: stats.Errors,
					})
					return stats, fmt.Errorf("checking duplicate for message %s: %w", stub.ID, dupErr)
				}
				if dup {
					stats.Duplicates++
					stats.RuleActionErrors += applyPostArchiveMSGraphRuleAction(ctx, client, action, stub.ID)
					onProgress.report(Progress{
						AccountID: accountID, FolderName: folder.FolderName, CurrentUID: nextUID,
						Processed: stats.Processed, Archived: stats.Archived, Errors: stats.Errors,
					})
					continue
				}
			}

			flags := msgraphFlagsFromStub(stub)
			archivedBytes, indexErr, archErr := ArchiveOne(ctx, raw, md, attachments, nextUID, flags, accountID, folder, mw, w, emailsRepo, foldersRepo, attachmentsRepo, idx, s3Queue)
			if archErr != nil {
				stats.Errors++
				onProgress.report(Progress{
					AccountID: accountID, FolderName: folder.FolderName, CurrentUID: nextUID,
					Processed: stats.Processed, Archived: stats.Archived, Errors: stats.Errors,
				})
				return stats, fmt.Errorf("archiving message %s: %w", stub.ID, archErr)
			}
			if indexErr != nil {
				stats.IndexErrors++
			}
			stats.RuleActionErrors += applyPostArchiveMSGraphRuleAction(ctx, client, action, stub.ID)

			folder.LastUID = nextUID
			nextUID++
			stats.Archived++
			stats.Bytes += archivedBytes
			onProgress.report(Progress{
				AccountID: accountID, FolderName: folder.FolderName, CurrentUID: nextUID,
				Processed: stats.Processed, Archived: stats.Archived, Errors: stats.Errors,
			})
		}

		if page.DeltaLink != "" {
			latestDeltaLink = page.DeltaLink
		}
		if page.NextLink == "" {
			break
		}
		link = page.NextLink
	}

	if err := foldersRepo.UpdateMSGraphDeltaLink(ctx, folder.ID, latestDeltaLink); err != nil {
		return stats, fmt.Errorf("persisting delta cursor: %w", err)
	}
	return stats, nil
}

// applyPostArchiveMSGraphRuleAction is applyPostArchiveRuleAction's MS
// Graph counterpart: archive_and_mark_read sets isRead=true,
// archive_and_delete deletes the message (Graph moves it to Deleted
// Items rather than erasing it outright — see msgraph.Client.Delete's
// own doc comment). Best-effort, same reasoning as the IMAP/Gmail
// versions: the message is already safely archived at this point, so a
// failure here is counted but never aborts the sync.
func applyPostArchiveMSGraphRuleAction(ctx context.Context, client *msgraph.Client, action domain.RuleAction, messageID string) int {
	switch action {
	case domain.ActionArchiveAndMarkRead:
		if err := client.MarkRead(ctx, messageID); err != nil {
			return 1
		}
	case domain.ActionArchiveAndDelete:
		if err := client.Delete(ctx, messageID); err != nil {
			return 1
		}
	}
	return 0
}

// msgraphFlagsFromStub maps a Graph message's isRead/flag state onto the
// IMAP flag vocabulary ArchiveOne and toMaildirFlags already understand,
// so an MS-Graph-archived message's Maildir filename and
// domain.Email.Flags carry the same read/flagged information an
// IMAP-synced copy of the same message would.
func msgraphFlagsFromStub(stub msgraph.MessageStub) []string {
	var flags []string
	if stub.IsRead {
		flags = append(flags, imap.SeenFlag)
	}
	if stub.Flag != nil && stub.Flag.FlagStatus == "flagged" {
		flags = append(flags, imap.FlaggedFlag)
	}
	return flags
}

// listMSGraphFoldersRecursive lists every top-level mail folder and
// recursively walks child folders, flattening nested names into
// "Parent/Child" — see SyncAccountMSGraph's doc comment.
func listMSGraphFoldersRecursive(ctx context.Context, client *msgraph.Client) ([]msgraphFolderRef, error) {
	top, err := client.ListMailFolders(ctx)
	if err != nil {
		return nil, err
	}
	var result []msgraphFolderRef
	for _, f := range top {
		if err := appendMSGraphFolderRecursive(ctx, client, f, f.DisplayName, &result); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func appendMSGraphFolderRecursive(ctx context.Context, client *msgraph.Client, f msgraph.MailFolder, flattenedName string, out *[]msgraphFolderRef) error {
	*out = append(*out, msgraphFolderRef{graphID: f.ID, name: flattenedName})
	if f.ChildFolderCount == 0 {
		return nil
	}
	children, err := client.ListChildFolders(ctx, f.ID)
	if err != nil {
		return err
	}
	for _, child := range children {
		if err := appendMSGraphFolderRecursive(ctx, client, child, flattenedName+"/"+child.DisplayName, out); err != nil {
			return err
		}
	}
	return nil
}
