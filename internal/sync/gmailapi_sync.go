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
	"github.com/yurydemin/marchi/internal/gmailapi"
	"github.com/yurydemin/marchi/internal/maildir"
	"github.com/yurydemin/marchi/internal/mimeparse"
	"github.com/yurydemin/marchi/internal/rules"
	"github.com/yurydemin/marchi/internal/search"
)

// gmailAllMailFolder is the single synthetic folder every
// ConnectorGmailAPI account's mail is archived under. Gmail is
// label-based — one message can carry several labels (INBOX, a custom
// label, and so on) at once — while Marchi's emails table assigns each
// message to exactly one folder. Rather than picking one "primary"
// label per message by some heuristic (and risking silently dropping a
// message that has no label Marchi recognizes), every message is
// archived exactly once under this folder, mirroring Gmail's own "All
// Mail" view — the default scope of users.messages.list with no
// labelIds filter (everything except Spam/Trash). Documented
// simplification, same category as joinRestoreFolder's delimiter
// assumption in internal/httpapi/restore_api.go.
const gmailAllMailFolder = "All Mail"

// gmailUIDValidity is the constant UIDVALIDITY the synthetic "All Mail"
// folder is upserted with — analogous to internal/importer's own
// constant (see cmd/marchi/cmd_import.go's importedAccountUIDValidity):
// nothing in the Gmail API is ever going to invalidate UID numbering
// Marchi itself assigns, so it never needs to change.
const gmailUIDValidity = 1

// SyncAccountGmailAPI is SyncAccount's counterpart for a
// ConnectorGmailAPI account: it fetches mail through the Gmail REST API
// (internal/gmailapi) instead of IMAP, into the "All Mail" folder
// created on first sync, running the same Rule Engine and Single-Writer
// ArchiveOne every IMAP-synced message goes through — client's access
// token must already be valid (refreshed if necessary; this function
// does not refresh it, matching how IMAP OAuth2 accounts already work —
// see internal/account.Manager.ResolveIMAPAuth's own doc comment).
//
// Incremental sync (a.GmailHistoryID as the cursor): an empty
// GmailHistoryID means no successful sync has ever completed — this run
// lists every message via ListMessages (paginated) instead of using the
// History API, and captures the historyId returned by GetProfile
// *before* that listing starts as the cursor to persist once this run
// completes. Otherwise it calls ListHistory(startHistoryId) and
// processes only the resulting messagesAdded — far cheaper than
// relisting the whole mailbox. If Gmail reports the cursor has aged out
// (gmailapi.ErrHistoryExpired — happens after roughly a week of
// inactivity per Gmail's own retention), this transparently falls back
// to a full listing, exactly like a never-synced account.
//
// Why capturing historyId *before* listing is safe: a message that
// arrives in the mailbox while a full listing is still in progress might
// or might not land in one of that listing's pages, depending on exact
// timing — but it's guaranteed to show up in a ListHistory call anchored
// to a historyId taken *before* the listing began, since Gmail's history
// log only grows forward from that point. So such a message is picked up
// exactly once, either this run (if the listing happened to include it —
// the Message-ID dedup below then correctly recognizes it as already
// archived next run) or next run (if it didn't) — never dropped either
// way.
//
// Resumability contract: unlike IMAP's FetchNewMessages, which advances
// folders.last_uid after every single message so a later message's
// failure can't undo already-committed progress, this function advances
// (persists) the GmailHistoryID cursor only once, after every message in
// this run's batch has been processed without error. The first
// unrecoverable error (a fetch/archive failure or a cancelled context)
// stops the batch immediately and leaves the cursor untouched — the next
// run re-derives the exact same batch (same ListHistory/ListMessages
// call, since nothing was persisted) and the Message-ID dedup check
// below correctly recognizes and skips whatever was already archived
// before the failure, so nothing is lost or reprocessed twice. This is
// the direct Gmail-API equivalent of IMAP's own "stop at first failure,
// resume from exactly there" contract (see FetchNewMessages' doc
// comment) — same guarantee, different mechanism, because a
// history-log-based cursor (unlike a UID watermark) can't be advanced
// message-by-message.
func SyncAccountGmailAPI(
	ctx context.Context,
	a *domain.Account,
	client *gmailapi.Client,
	maildirRoot, host string,
	w writer.Writer,
	accountsRepo *repo.AccountsRepo,
	foldersRepo *repo.FoldersRepo,
	emailsRepo *repo.EmailsRepo,
	attachmentsRepo *repo.AttachmentsRepo,
	syncLogsRepo *repo.SyncLogsRepo,
	rulesRepo *repo.RulesRepo, // nil skips Rule Engine dispatch entirely — every message defaults to archive (FR-RE-03)
	idx *search.Index, // nil skips search indexing entirely — see ArchiveOne
	s3ConfigRepo *repo.S3ConfigRepo,
	s3QueueRepo *repo.S3UploadQueueRepo,
	onProgress ProgressFunc, // nil skips progress reporting entirely (FR-SE-07)
) (FolderResult, error) {
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

	folder, err := foldersRepo.UpsertFolder(ctx, a.ID, gmailAllMailFolder, gmailUIDValidity)
	if err != nil {
		syncErr = fmt.Errorf("gmailapi sync: preparing folder: %w", err)
		return FolderResult{}, syncErr
	}

	dir := maildir.FolderDir(maildirRoot, a.ID, maildir.SafeFolderName(folder.FolderName))
	layout, err := maildir.NewLayout(dir)
	if err != nil {
		syncErr = fmt.Errorf("gmailapi sync: preparing maildir: %w", err)
		return FolderResult{Folder: folder}, syncErr
	}
	mw := maildir.NewWriter(layout, host)

	var activeRules []*domain.Rule
	if rulesRepo != nil {
		activeRules, err = rulesRepo.ListActive(ctx)
		if err != nil {
			syncErr = fmt.Errorf("gmailapi sync: loading active rules: %w", err)
			return FolderResult{Folder: folder}, syncErr
		}
	}

	var s3Queue *repo.S3UploadQueueRepo
	if s3ConfigRepo != nil && s3QueueRepo != nil {
		s3cfg, err := s3ConfigRepo.Get(ctx)
		if err == nil && s3cfg.Enabled {
			s3Queue = s3QueueRepo
		} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
			syncErr = fmt.Errorf("gmailapi sync: loading s3 config: %w", err)
			return FolderResult{Folder: folder}, syncErr
		}
	}

	ids, nextHistoryID, err := resolveMessageIDs(ctx, client, a.GmailHistoryID)
	if err != nil {
		syncErr = fmt.Errorf("gmailapi sync: listing messages: %w", err)
		return FolderResult{Folder: folder}, syncErr
	}

	nextUID := folder.LastUID + 1
	estimatedTotal := len(ids)
	var firstErr error
	for _, id := range ids {
		if ctx.Err() != nil {
			firstErr = ctx.Err()
			break
		}
		total.Processed++

		msg, fetchErr := client.GetMessageRaw(ctx, id)
		if fetchErr != nil {
			total.Errors++
			firstErr = fmt.Errorf("gmailapi sync: fetching message %s: %w", id, fetchErr)
			onProgress.report(Progress{
				AccountID: a.ID, FolderName: folder.FolderName, CurrentUID: nextUID,
				Total: estimatedTotal, Processed: total.Processed, Archived: total.Archived, Errors: total.Errors,
			})
			break
		}
		raw, rawErr := msg.RawBytes()
		if rawErr != nil {
			total.Errors++
			firstErr = fmt.Errorf("gmailapi sync: decoding message %s: %w", id, rawErr)
			onProgress.report(Progress{
				AccountID: a.ID, FolderName: folder.FolderName, CurrentUID: nextUID,
				Total: estimatedTotal, Processed: total.Processed, Archived: total.Archived, Errors: total.Errors,
			})
			break
		}

		md := mimeparse.Parse(raw)
		attachments := mimeparse.ParseAttachments(raw)

		action := domain.ActionArchive
		if matched := rules.FirstMatch(activeRules, candidateFrom(md, attachments, raw, folder.FolderName, a.ID)); matched != nil {
			action = matched.Action
			total.addRuleMatch(matched.ID)
		}
		if action == domain.ActionSkip {
			total.Skipped++
			onProgress.report(Progress{
				AccountID: a.ID, FolderName: folder.FolderName, CurrentUID: nextUID,
				Total: estimatedTotal, Processed: total.Processed, Archived: total.Archived, Errors: total.Errors,
			})
			continue
		}

		if md.MessageID != "" {
			dup, dupErr := emailsRepo.ExistsByAccountMessageID(ctx, a.ID, md.MessageID)
			if dupErr != nil {
				total.Errors++
				firstErr = fmt.Errorf("gmailapi sync: checking duplicate for message %s: %w", id, dupErr)
				onProgress.report(Progress{
					AccountID: a.ID, FolderName: folder.FolderName, CurrentUID: nextUID,
					Total: estimatedTotal, Processed: total.Processed, Archived: total.Archived, Errors: total.Errors,
				})
				break
			}
			if dup {
				total.Duplicates++
				total.RuleActionErrors += applyPostArchiveGmailRuleAction(ctx, client, action, id)
				onProgress.report(Progress{
					AccountID: a.ID, FolderName: folder.FolderName, CurrentUID: nextUID,
					Total: estimatedTotal, Processed: total.Processed, Archived: total.Archived, Errors: total.Errors,
				})
				continue
			}
		}

		flags := gmailFlagsFromLabels(msg.LabelIDs)
		archivedBytes, indexErr, archErr := ArchiveOne(ctx, raw, md, attachments, nextUID, flags, a.ID, folder, mw, w, emailsRepo, foldersRepo, attachmentsRepo, idx, s3Queue)
		if archErr != nil {
			total.Errors++
			firstErr = fmt.Errorf("gmailapi sync: archiving message %s: %w", id, archErr)
			onProgress.report(Progress{
				AccountID: a.ID, FolderName: folder.FolderName, CurrentUID: nextUID,
				Total: estimatedTotal, Processed: total.Processed, Archived: total.Archived, Errors: total.Errors,
			})
			break
		}
		if indexErr != nil {
			total.IndexErrors++
		}
		total.RuleActionErrors += applyPostArchiveGmailRuleAction(ctx, client, action, id)

		folder.LastUID = nextUID
		nextUID++
		total.Archived++
		total.Bytes += archivedBytes
		onProgress.report(Progress{
			AccountID: a.ID, FolderName: folder.FolderName, CurrentUID: nextUID,
			Total: estimatedTotal, Processed: total.Processed, Archived: total.Archived, Errors: total.Errors,
		})
	}

	if firstErr == nil {
		if err := accountsRepo.UpdateGmailHistoryID(ctx, a.ID, nextHistoryID); err != nil {
			firstErr = fmt.Errorf("gmailapi sync: persisting history cursor: %w", err)
		}
		// Best-effort, same convention as search indexing and the audit
		// log — see internal/sync/account.go's identical call for the
		// full reasoning. Gated on firstErr == nil for the same reason
		// the history cursor above is: a message whose match wasn't
		// recorded because this run failed gets naturally re-evaluated
		// (and re-recorded) on the retry that re-lists the same
		// unadvanced-cursor batch, so recording it twice here would
		// double-count it.
		if rulesRepo != nil {
			_ = rulesRepo.RecordMatches(ctx, total.RuleMatches)
		}
	}

	syncErr = firstErr
	return FolderResult{Folder: folder, Fetched: total.Archived}, firstErr
}

// applyPostArchiveGmailRuleAction is applyPostArchiveRuleAction's Gmail
// API counterpart: archive_and_mark_read removes Gmail's UNREAD label
// (Gmail has no separate "read" flag, just that label's absence),
// archive_and_delete moves the message to Trash (recoverable for 30
// days — the same "not instantly unrecoverable" spirit as the IMAP
// path's \Deleted+expunge). Best-effort, same reasoning as the IMAP
// version: the message is already safely archived at this point, so a
// failure here is counted but never aborts the sync.
func applyPostArchiveGmailRuleAction(ctx context.Context, client *gmailapi.Client, action domain.RuleAction, messageID string) int {
	switch action {
	case domain.ActionArchiveAndMarkRead:
		if err := client.ModifyLabels(ctx, messageID, nil, []string{"UNREAD"}); err != nil {
			return 1
		}
	case domain.ActionArchiveAndDelete:
		if err := client.Trash(ctx, messageID); err != nil {
			return 1
		}
	}
	return 0
}

// gmailFlagsFromLabels maps Gmail's label-based read/starred/etc. state
// onto the IMAP flag vocabulary ArchiveOne and toMaildirFlags already
// understand, so a Gmail-API-archived message's Maildir filename and
// domain.Email.Flags carry the same read/flagged information an
// IMAP-synced copy of the same message would.
func gmailFlagsFromLabels(labelIDs []string) []string {
	has := func(label string) bool {
		for _, l := range labelIDs {
			if l == label {
				return true
			}
		}
		return false
	}
	var flags []string
	if !has("UNREAD") {
		flags = append(flags, imap.SeenFlag)
	}
	if has("STARRED") {
		flags = append(flags, imap.FlaggedFlag)
	}
	if has("DRAFT") {
		flags = append(flags, imap.DraftFlag)
	}
	return flags
}

// resolveMessageIDs returns every message ID this sync run should
// process, plus the historyId the caller should persist as the
// account's cursor once the run completes without error. See
// SyncAccountGmailAPI's doc comment for the full incremental-sync/cursor
// contract this implements.
func resolveMessageIDs(ctx context.Context, client *gmailapi.Client, currentHistoryID string) (ids []string, nextHistoryID string, err error) {
	if currentHistoryID != "" {
		ids, nextHistoryID, err = listHistoryMessageIDs(ctx, client, currentHistoryID)
		switch {
		case err == nil:
			return ids, nextHistoryID, nil
		case errors.Is(err, gmailapi.ErrHistoryExpired):
			// Fall through to a full resync below.
		default:
			return nil, "", err
		}
	}

	profile, err := client.GetProfile(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("getting profile for history cursor: %w", err)
	}
	ids, err = listAllMessageIDs(ctx, client)
	if err != nil {
		return nil, "", err
	}
	return ids, profile.HistoryID, nil
}

func listAllMessageIDs(ctx context.Context, client *gmailapi.Client) ([]string, error) {
	var ids []string
	pageToken := ""
	for {
		if ctx.Err() != nil {
			return ids, ctx.Err()
		}
		list, err := client.ListMessages(ctx, pageToken)
		if err != nil {
			return nil, err
		}
		for _, m := range list.Messages {
			ids = append(ids, m.ID)
		}
		if list.NextPageToken == "" {
			return ids, nil
		}
		pageToken = list.NextPageToken
	}
}

func listHistoryMessageIDs(ctx context.Context, client *gmailapi.Client, startHistoryID string) (ids []string, latestHistoryID string, err error) {
	latestHistoryID = startHistoryID
	pageToken := ""
	for {
		if ctx.Err() != nil {
			return ids, latestHistoryID, ctx.Err()
		}
		list, err := client.ListHistory(ctx, startHistoryID, pageToken)
		if err != nil {
			return nil, "", err
		}
		for _, rec := range list.History {
			for _, added := range rec.MessagesAdded {
				ids = append(ids, added.Message.ID)
			}
		}
		if list.HistoryID != "" {
			latestHistoryID = list.HistoryID
		}
		if list.NextPageToken == "" {
			return ids, latestHistoryID, nil
		}
		pageToken = list.NextPageToken
	}
}
