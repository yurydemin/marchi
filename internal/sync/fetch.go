package sync

import (
	"context"
	"database/sql"
	"fmt"
	"io"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"

	"github.com/yurydemin/marchi/internal/db/repo"
	"github.com/yurydemin/marchi/internal/db/writer"
	"github.com/yurydemin/marchi/internal/domain"
	"github.com/yurydemin/marchi/internal/imapclient"
	"github.com/yurydemin/marchi/internal/maildir"
	"github.com/yurydemin/marchi/internal/mimeparse"
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
// (Archived successes + Errors failures) — messages drained from the
// channel without being touched, after the first failure, don't count as
// processed since they were never examined at all.
type FetchStats struct {
	Processed int
	Archived  int
	Bytes     int64
	Errors    int
}

// FetchNewMessages selects folder on c (read-only — this is an archiver,
// never a source of mutations back to the account it's archiving) and
// fetches every message with UID greater than folder.LastUID (FR-SE-01).
// Rule Engine dispatch (FR-RE-*) isn't wired in yet — every message is
// archived unconditionally, matching this step's stated scope.
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

	status, err := c.Select(rawName, true)
	if err != nil {
		return FetchStats{}, fmt.Errorf("sync: SELECT %q: %w", folder.FolderName, err)
	}

	startUID := uint64(folder.LastUID) + 1
	if startUID >= uint64(status.UidNext) {
		return FetchStats{}, nil // nothing new
	}

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
		archivedBytes, archErr := archiveOne(ctx, msg, section, accountID, folder, mw, w, emailsRepo, foldersRepo, attachmentsRepo)
		if archErr != nil {
			stats.Errors++
			firstErr = fmt.Errorf("sync: archiving UID %d in %q: %w", msg.Uid, folder.FolderName, archErr)
			continue
		}
		folder.LastUID = msg.Uid // keep the in-memory Folder in sync with what was just committed to the DB
		stats.Archived++
		stats.Bytes += archivedBytes
	}
	if fetchErr := <-done; fetchErr != nil && firstErr == nil {
		firstErr = fmt.Errorf("sync: UID FETCH in %q: %w", folder.FolderName, fetchErr)
	}
	return stats, firstErr
}

// archiveOne implements NFR-RL-03's atomicity contract for a single
// message: stage the raw bytes into Maildir tmp/, insert the emails row and
// advance folders.last_uid in one Single-Writer transaction, and only then
// commit tmp/ into new/. If staging or the SQL transaction fails, nothing
// is left committed and the UID isn't advanced — the next sync retries it.
func archiveOne(
	ctx context.Context,
	msg *imap.Message,
	section *imap.BodySectionName,
	accountID int64,
	folder *domain.Folder,
	mw *maildir.Writer,
	w writer.Writer,
	emailsRepo *repo.EmailsRepo,
	foldersRepo *repo.FoldersRepo,
	attachmentsRepo *repo.AttachmentsRepo,
) (bytesArchived int64, err error) {
	body := msg.GetBody(section)
	if body == nil {
		return 0, fmt.Errorf("server returned no body")
	}
	raw, err := io.ReadAll(body)
	if err != nil {
		return 0, fmt.Errorf("reading body: %w", err)
	}

	md := mimeparse.Parse(raw)
	messageID := md.MessageID
	if messageID == "" {
		messageID = fmt.Sprintf("<no-message-id-%d-%d-%d@mailvault.local>", accountID, folder.ID, msg.Uid)
	}
	attachments := mimeparse.ParseAttachments(raw)

	maildirFlags := toMaildirFlags(msg.Flags)
	tmpPath, err := mw.Stage(raw, maildirFlags)
	if err != nil {
		return 0, fmt.Errorf("staging: %w", err)
	}

	email := &domain.Email{
		MessageID:       messageID,
		AccountID:       accountID,
		FolderID:        folder.ID,
		UID:             msg.Uid,
		Subject:         md.Subject,
		FromAddr:        md.From,
		ToAddrs:         md.To,
		CcAddrs:         md.Cc,
		Date:            md.Date,
		Size:            int64(len(raw)),
		HasAttachments:  len(attachments) > 0,
		Flags:           msg.Flags,
		StorageLocation: "local",
		LocalPath:       mw.FinalPath(tmpPath),
	}

	err = w.Do(ctx, func(tx *sql.Tx) error {
		emailID, err := emailsRepo.Insert(ctx, tx, email)
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
		return foldersRepo.UpdateLastUID(ctx, tx, folder.ID, msg.Uid)
	})
	if err != nil {
		_ = mw.Discard(tmpPath)
		return 0, fmt.Errorf("persisting: %w", err)
	}

	if _, err := mw.Commit(tmpPath); err != nil {
		// SQLite already committed at this point — the emails row now
		// references a path that doesn't exist in new/ yet. Rare (a same-
		// filesystem rename failing right after a successful write is an
		// edge case, not a routine one) but surfaced as an error rather
		// than swallowed, since it needs an operator's attention.
		return 0, fmt.Errorf("committing maildir file (SQLite already committed for UID %d): %w", msg.Uid, err)
	}
	return int64(len(raw)), nil
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
