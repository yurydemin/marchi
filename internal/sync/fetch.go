package sync

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"

	"github.com/yurydemin/marchi/internal/db/repo"
	"github.com/yurydemin/marchi/internal/db/writer"
	"github.com/yurydemin/marchi/internal/domain"
	"github.com/yurydemin/marchi/internal/imapclient"
	"github.com/yurydemin/marchi/internal/maildir"
	"github.com/yurydemin/marchi/internal/mimeparse"
	"github.com/yurydemin/marchi/internal/rules"
	"github.com/yurydemin/marchi/internal/s3store"
	"github.com/yurydemin/marchi/internal/search"
)

// imapToMaildirFlag translates the IMAP system flags into their Maildir
// info-section letters. \Recent has no Maildir equivalent and is dropped —
// it's a per-connection "is this new since your last SELECT" marker, not a
// durable property of the message.
var imapToMaildirFlag = map[string]byte{
	imap.SeenFlag:     'S',
	imap.AnsweredFlag: 'R',
	imap.FlaggedFlag:  'F',
	imap.DeletedFlag:  'T',
	imap.DraftFlag:    'D',
}

// FetchStats summarizes one folder's fetch pass, for sync_logs aggregation
// (FR-SE-06/07). Processed counts every message actually attempted
// (Archived successes + Skipped + Errors failures) — messages drained from
// the channel without being touched, after the first failure, don't count
// as processed since they were never examined at all.
type FetchStats struct {
	Processed int
	Archived  int
	// Skipped counts messages a rule's "skip" action kept out of the
	// archive entirely (FR-RE-03) — last_uid still advances past them
	// (see skipOne), so they're not reprocessed on the next sync.
	Skipped int
	Bytes   int64
	Errors  int
	// IndexErrors counts search-index (Bluge) write failures — best-effort
	// (see archiveOne's doc comment): the email is still fully archived
	// and this never contributes to Errors or stops the sync. A reindex
	// (FR-SR-04) is the recovery path if this is ever nonzero.
	IndexErrors int
	// RuleActionErrors counts archive_and_delete/archive_and_mark_read
	// IMAP side effects (STORE/EXPUNGE on the source server) that failed
	// after the message was already successfully archived. Best-effort,
	// same reasoning as IndexErrors: the archive copy is the thing that
	// must not be lost, so a failure here is logged and counted, not
	// treated as fatal — the source message just keeps whatever flag it
	// already had.
	RuleActionErrors int
}

// FetchNewMessages selects folder on c and fetches every message with UID
// greater than folder.LastUID (FR-SE-01). The mailbox is opened
// read-write (not EXAMINE) so archive_and_delete/archive_and_mark_read
// actions can issue STORE/EXPUNGE — fetching itself still can't mark a
// message \Seen as a side effect regardless of that mode, since every
// FETCH here uses BODY.PEEK[].
//
// activeRules (FR-RE-01..03), pre-loaded and sorted by priority by the
// caller (see repo.RulesRepo.ListActive), is evaluated against each
// message's parsed metadata before it's archived. The first matching
// rule's Action decides what happens; no match (including a nil/empty
// activeRules) defaults to archive — the same unconditional behavior
// Phase 1/2 always had.
//
// Messages are processed in the order the server returns them (ascending
// UID for any well-behaved server) and stop at the first failure rather
// than skipping past it: last_uid is a single watermark, not a sparse set
// of processed UIDs, so skipping a failed message would advance the
// watermark past it and silently lose it on every future sync. Stopping
// early means the next sync naturally retries from exactly where this one
// left off (NFR-RL-02).
func FetchNewMessages(
	ctx context.Context,
	c *client.Client,
	accountID int64,
	folder *domain.Folder,
	mw *maildir.Writer,
	w writer.Writer,
	emailsRepo *repo.EmailsRepo,
	foldersRepo *repo.FoldersRepo,
	attachmentsRepo *repo.AttachmentsRepo,
	idx *search.Index, // nil skips indexing entirely — see archiveOne
	s3Queue *repo.S3UploadQueueRepo, // nil skips S3 mirror enqueueing entirely (FR-S3-03) — see archiveOne
	activeRules []*domain.Rule, // nil/empty: every message defaults to archive (FR-RE-03)
	onProgress ProgressFunc, // nil skips progress reporting entirely (FR-SE-07)
) (stats FetchStats, err error) {
	if !folder.SyncEnabled {
		return FetchStats{}, nil
	}
	if ctx.Err() != nil { // graceful shutdown already requested (NFR-RL-05): skip this folder entirely
		return FetchStats{}, ctx.Err()
	}

	rawName, err := imapclient.EncodeFolderName(folder.FolderName)
	if err != nil {
		return FetchStats{}, err
	}

	status, err := c.Select(rawName, false)
	if err != nil {
		return FetchStats{}, fmt.Errorf("sync: SELECT %q: %w", folder.FolderName, err)
	}

	startUID := uint64(folder.LastUID) + 1
	if startUID >= uint64(status.UidNext) {
		return FetchStats{}, nil // nothing new
	}
	// Best-effort estimate: the actual count can differ slightly if
	// messages are deleted between this SELECT and the UID FETCH below,
	// but it's close enough for a progress indicator (FR-SE-07's "всего
	// писем").
	estimatedTotal := int(uint64(status.UidNext) - startUID)

	seqset := new(imap.SeqSet)
	seqset.AddRange(uint32(startUID), 0) // 0 == "*", the highest UID present

	section := &imap.BodySectionName{Peek: true} // BODY.PEEK[]: fetching must not mark \Seen
	items := []imap.FetchItem{imap.FetchUid, imap.FetchFlags, section.FetchItem()}

	messages := make(chan *imap.Message, 16)
	done := make(chan error, 1)
	go func() { done <- c.UidFetch(seqset, items, messages) }()

	var firstErr error
	for msg := range messages {
		if firstErr != nil {
			continue // drain the rest without processing, preserving UID order
		}
		// Graceful shutdown (NFR-RL-05): stop starting new work once a
		// SIGINT/SIGTERM has been requested, but don't interrupt whatever
		// archiveOne call might already be in flight — Single Writer's own
		// Close() (called during the CLI command's unwind) already waits
		// for that one to finish rather than cutting it off mid-write.
		if ctx.Err() != nil {
			firstErr = ctx.Err()
			continue
		}
		stats.Processed++

		raw, md, attachments, parseErr := parseMessage(msg, section)
		if parseErr != nil {
			stats.Errors++
			firstErr = fmt.Errorf("sync: reading UID %d in %q: %w", msg.Uid, folder.FolderName, parseErr)
			onProgress.report(Progress{
				AccountID: accountID, FolderName: folder.FolderName, CurrentUID: msg.Uid,
				Total: estimatedTotal, Processed: stats.Processed, Archived: stats.Archived, Errors: stats.Errors,
			})
			continue
		}

		action := domain.ActionArchive
		if matched := rules.FirstMatch(activeRules, candidateFrom(md, attachments, raw, folder.FolderName, accountID)); matched != nil {
			action = matched.Action
		}

		if action == domain.ActionSkip {
			if skipErr := skipOne(ctx, w, foldersRepo, folder.ID, msg.Uid); skipErr != nil {
				stats.Errors++
				firstErr = fmt.Errorf("sync: skipping UID %d in %q: %w", msg.Uid, folder.FolderName, skipErr)
				onProgress.report(Progress{
					AccountID: accountID, FolderName: folder.FolderName, CurrentUID: msg.Uid,
					Total: estimatedTotal, Processed: stats.Processed, Archived: stats.Archived, Errors: stats.Errors,
				})
				continue
			}
			folder.LastUID = msg.Uid
			stats.Skipped++
			onProgress.report(Progress{
				AccountID: accountID, FolderName: folder.FolderName, CurrentUID: msg.Uid,
				Total: estimatedTotal, Processed: stats.Processed, Archived: stats.Archived, Errors: stats.Errors,
			})
			continue
		}

		archivedBytes, indexErr, archErr := archiveOne(ctx, raw, md, attachments, msg.Uid, msg.Flags, accountID, folder, mw, w, emailsRepo, foldersRepo, attachmentsRepo, idx, s3Queue)
		if archErr != nil {
			stats.Errors++
			firstErr = fmt.Errorf("sync: archiving UID %d in %q: %w", msg.Uid, folder.FolderName, archErr)
			onProgress.report(Progress{
				AccountID: accountID, FolderName: folder.FolderName, CurrentUID: msg.Uid,
				Total: estimatedTotal, Processed: stats.Processed, Archived: stats.Archived, Errors: stats.Errors,
			})
			continue
		}
		if indexErr != nil {
			stats.IndexErrors++
		}

		// Post-archive IMAP side effects (FR-RE-03). Best-effort: the
		// message is already safely archived at this point, so a failure
		// here (logged via RuleActionErrors, not surfaced as firstErr)
		// leaves the source message as-is rather than jeopardizing
		// progress already made.
		switch action {
		case domain.ActionArchiveAndMarkRead:
			if seenErr := imapclient.MarkSeen(c, msg.Uid); seenErr != nil {
				stats.RuleActionErrors++
			}
		case domain.ActionArchiveAndDelete:
			if delErr := imapclient.DeleteMessage(c, msg.Uid); delErr != nil {
				stats.RuleActionErrors++
			}
		}

		folder.LastUID = msg.Uid // keep the in-memory Folder in sync with what was just committed to the DB
		stats.Archived++
		stats.Bytes += archivedBytes
		onProgress.report(Progress{
			AccountID: accountID, FolderName: folder.FolderName, CurrentUID: msg.Uid,
			Total: estimatedTotal, Processed: stats.Processed, Archived: stats.Archived, Errors: stats.Errors,
		})
	}
	if fetchErr := <-done; fetchErr != nil && firstErr == nil {
		firstErr = fmt.Errorf("sync: UID FETCH in %q: %w", folder.FolderName, fetchErr)
	}
	return stats, firstErr
}

// parseMessage reads msg's full body and extracts the metadata/attachment
// list archiveOne needs to persist it and FirstMatch needs to evaluate
// rules against it — done once per message up front so a "skip" verdict
// never touches Maildir/SQL/the index at all.
func parseMessage(msg *imap.Message, section *imap.BodySectionName) (raw []byte, md mimeparse.Metadata, attachments []mimeparse.Attachment, err error) {
	body := msg.GetBody(section)
	if body == nil {
		return nil, mimeparse.Metadata{}, nil, fmt.Errorf("server returned no body")
	}
	raw, err = io.ReadAll(body)
	if err != nil {
		return nil, mimeparse.Metadata{}, nil, fmt.Errorf("reading body: %w", err)
	}
	md = mimeparse.Parse(raw)
	attachments = mimeparse.ParseAttachments(raw)
	return raw, md, attachments, nil
}

// candidateFrom builds the rules.Candidate FirstMatch evaluates a
// message's condition trees against, straight from parseMessage's output
// — no separate parsing pass.
func candidateFrom(md mimeparse.Metadata, attachments []mimeparse.Attachment, raw []byte, folderName string, accountID int64) rules.Candidate {
	types := make([]string, len(attachments))
	for i, a := range attachments {
		types[i] = a.MIMEType
	}
	return rules.Candidate{
		From: md.From, FromAddr: md.FromAddr,
		To: md.To, ToAddrs: md.ToAddrs,
		Cc: md.Cc, CcAddrs: md.CcAddrs,
		Subject:         md.Subject,
		HasAttachments:  len(attachments) > 0,
		AttachmentTypes: types,
		Size:            int64(len(raw)),
		Date:            md.Date,
		FolderName:      folderName,
		AccountID:       accountID,
	}
}

// skipOne advances folderID's last_uid past uid without archiving
// anything (FR-RE-03's skip action) — a message a rule skips must still
// not be re-fetched and re-evaluated on every future sync.
func skipOne(ctx context.Context, w writer.Writer, foldersRepo *repo.FoldersRepo, folderID int64, uid uint32) error {
	return w.Do(ctx, func(tx *sql.Tx) error {
		return foldersRepo.UpdateLastUID(ctx, tx, folderID, uid)
	})
}

// archiveOne implements NFR-RL-03's atomicity contract for a single
// message: stage the raw bytes into Maildir tmp/, commit it into new/, and
// only then insert the emails row and advance folders.last_uid in one
// Single-Writer transaction.
//
// This order — Maildir commit before the SQL write — is deliberately the
// reverse of an earlier draft (staging in tmp/ was always going to precede
// SQL, per correction #10, but this step's crash-recovery test caught that
// committing SQL *before* the Maildir rename has a real data-loss window: a
// crash between the SQL commit and the rename leaves a committed emails row
// (with last_uid already advanced past it) pointing at a file still sitting
// in tmp/, and the next startup's tmp/ sweep deletes it — the row survives,
// the content doesn't, and since last_uid already moved past this UID nothing
// ever re-fetches it. Doing the rename first instead means the only failure
// window left is the SQL write failing *after* the file already landed in
// new/, which just leaves one orphaned, untracked file behind (no sweep
// mechanism cleans new/) while last_uid stays put — annoying, not lossy: the
// next sync just re-fetches and re-archives the same message under a fresh
// filename. Between "silent data loss" and "a stray file", the second is the
// only acceptable failure mode for an archiver.
//
// Search indexing (idx, FR-SR-01/02) happens last, after the SQL
// transaction commits, and is deliberately best-effort: Bluge has no way
// to join a transaction with SQLite, so treating an index write failure as
// fatal here would reopen exactly the kind of crash-window problem this
// function's Maildir/SQL reordering just fixed, except now across three
// heterogeneous stores instead of two. A failed index write is reported
// back via the indexErr return (the caller counts it in
// FetchStats.IndexErrors) but never fails the archival itself — the email
// row and file are the source of truth, and FR-SR-04's full reindex is the
// recovery path if the index ever falls behind.
//
// The S3 mirror upload (s3Queue, FR-S3-03) is different from search
// indexing in one important way: s3_upload_queue lives in the same
// SQLite database as emails, so there's no cross-store atomicity problem
// to work around. Its INSERT rides inside the very same Single-Writer
// transaction as the emails/attachments/last_uid writes below — if S3 is
// enabled, the queue row and the email row are created atomically
// together, or neither is, rather than being merely best-effort like the
// index write has to be. The actual upload happens later, out of band,
// in the Uploader worker pool (internal/s3store).
func archiveOne(
	ctx context.Context,
	raw []byte,
	md mimeparse.Metadata,
	attachments []mimeparse.Attachment,
	uid uint32,
	flags []string,
	accountID int64,
	folder *domain.Folder,
	mw *maildir.Writer,
	w writer.Writer,
	emailsRepo *repo.EmailsRepo,
	foldersRepo *repo.FoldersRepo,
	attachmentsRepo *repo.AttachmentsRepo,
	idx *search.Index,
	s3Queue *repo.S3UploadQueueRepo,
) (bytesArchived int64, indexErr error, err error) {
	messageID := md.MessageID
	if messageID == "" {
		messageID = fmt.Sprintf("<no-message-id-%d-%d-%d@mailvault.local>", accountID, folder.ID, uid)
	}
	bodyText := mimeparse.ParseBody(raw)

	maildirFlags := toMaildirFlags(flags)
	tmpPath, err := mw.Stage(raw, maildirFlags)
	if err != nil {
		return 0, nil, fmt.Errorf("staging: %w", err)
	}

	finalPath, err := mw.Commit(tmpPath)
	if err != nil {
		_ = mw.Discard(tmpPath)
		return 0, nil, fmt.Errorf("committing maildir file: %w", err)
	}

	email := &domain.Email{
		MessageID:       messageID,
		AccountID:       accountID,
		FolderID:        folder.ID,
		UID:             uid,
		Subject:         md.Subject,
		FromAddr:        md.From,
		ToAddrs:         md.To,
		CcAddrs:         md.Cc,
		Date:            md.Date,
		Size:            int64(len(raw)),
		HasAttachments:  len(attachments) > 0,
		Flags:           flags,
		StorageLocation: "local",
		LocalPath:       finalPath,
	}

	var emailID int64
	err = w.Do(ctx, func(tx *sql.Tx) error {
		var err error
		emailID, err = emailsRepo.Insert(ctx, tx, email)
		if err != nil {
			return err
		}
		for _, att := range attachments {
			if _, err := attachmentsRepo.Insert(ctx, tx, &domain.Attachment{
				EmailID:   emailID,
				Filename:  att.Filename,
				MIMEType:  att.MIMEType,
				Size:      att.Size,
				ContentID: att.ContentID,
			}); err != nil {
				return err
			}
		}
		if s3Queue != nil {
			keyDate := md.Date
			if keyDate.IsZero() {
				keyDate = time.Now().UTC()
			}
			s3Key := s3store.EmailKey(accountID, keyDate, s3store.ContentSHA256Hex(raw))
			if err := s3Queue.Enqueue(ctx, tx, emailID, s3Key, finalPath); err != nil {
				return err
			}
		}
		return foldersRepo.UpdateLastUID(ctx, tx, folder.ID, uid)
	})
	if err != nil {
		// The file is already committed into new/ at this point — it's now
		// an orphan (see the doc comment above for why that's the accepted
		// tradeoff), not a dangling DB reference. Nothing to roll back on
		// the filesystem side; the next sync retries this UID from scratch.
		return 0, nil, fmt.Errorf("persisting (file already archived to %s but not yet in SQLite — will be retried and re-archived under a new name): %w", finalPath, err)
	}

	if idx != nil {
		indexErr = idx.Index(search.Doc{
			EmailID:         emailID,
			MessageID:       messageID,
			Subject:         md.Subject,
			From:            md.From,
			FromAddr:        md.FromAddr,
			To:              md.To,
			ToAddrs:         md.ToAddrs,
			Cc:              md.Cc,
			CcAddrs:         md.CcAddrs,
			Body:            bodyText,
			AttachmentNames: attachmentNames(attachments),
			Date:            md.Date,
			AccountID:       accountID,
			FolderID:        folder.ID,
			HasAttachments:  len(attachments) > 0,
			Size:            int64(len(raw)),
		})
	}

	return int64(len(raw)), indexErr, nil
}

func attachmentNames(attachments []mimeparse.Attachment) []string {
	if len(attachments) == 0 {
		return nil
	}
	names := make([]string, len(attachments))
	for i, a := range attachments {
		names[i] = a.Filename
	}
	return names
}

func toMaildirFlags(imapFlags []string) string {
	var b []byte
	for _, f := range imapFlags {
		if c, ok := imapToMaildirFlag[f]; ok {
			b = append(b, c)
		}
	}
	return string(b)
}
