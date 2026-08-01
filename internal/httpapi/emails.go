package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/yurydemin/marchi/internal/domain"
	"github.com/yurydemin/marchi/internal/maildir"
	"github.com/yurydemin/marchi/internal/mimeparse"
)

// registerEmails wires the Emails REST API (FR-API-02, FR-VW-01/02):
// metadata + sanitized body preview, downloading the original .eml,
// downloading an individual attachment, and deleting one or many emails.
func registerEmails(app *fiber.App, vault *vaultState) {
	app.Get("/api/v1/emails/:id", handleGetEmail(vault))
	app.Get("/api/v1/emails/:id/download", handleDownloadEmail(vault))
	app.Get("/api/v1/emails/:id/attachments/:att_id/download", handleDownloadAttachment(vault))
	app.Delete("/api/v1/emails/:id", handleDeleteEmail(vault))
	app.Post("/api/v1/emails/bulk-delete", handleBulkDeleteEmails(vault))
	app.Post("/api/v1/emails/bulk-folder", handleBulkFolderMove(vault))
}

type attachmentResponse struct {
	ID       int64  `json:"id"`
	Filename string `json:"filename"`
	MIMEType string `json:"mime_type"`
	Size     int64  `json:"size"`
}

type emailResponse struct {
	ID              int64                `json:"id"`
	MessageID       string               `json:"message_id"`
	Subject         string               `json:"subject"`
	From            string               `json:"from"`
	To              []string             `json:"to"`
	Cc              []string             `json:"cc"`
	Date            time.Time            `json:"date"`
	AccountID       int64                `json:"account_id"`
	FolderID        int64                `json:"folder_id"`
	Size            int64                `json:"size"`
	HasAttachments  bool                 `json:"has_attachments"`
	StorageLocation string               `json:"storage_location"`
	Attachments     []attachmentResponse `json:"attachments"`
	BodyHTML        string               `json:"body_html,omitempty"` // sanitized (FR-VW-01) — empty if the message had no HTML part
	BodyText        string               `json:"body_text,omitempty"` // fallback when body_html is empty
}

// handleGetEmail returns metadata plus a body preview (FR-VW-01: headers,
// sanitized HTML falling back to plain text, attachment list). The
// preview is best-effort: if the content can't be loaded (S3-resident but
// S3 isn't configured, or the local .eml was somehow removed from disk),
// metadata and the attachment list are still returned, just without
// BodyHTML/BodyText — a broken preview shouldn't hide everything else
// that's still known about the email. S3-resident emails are transparently
// lazy-loaded (FR-RS-03) the same as a local read, just slower on a cache miss.
func handleGetEmail(vault *vaultState) fiber.Handler {
	return func(c *fiber.Ctx) error {
		b, err := currentBackendOrLocked(vault)
		if err != nil {
			return err
		}
		id, err := idParam(c, "id")
		if err != nil {
			return err
		}

		e, err := b.emailsRepo.GetByID(c.Context(), id)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fiber.NewError(fiber.StatusNotFound, "email not found")
			}
			return fiber.NewError(fiber.StatusInternalServerError, "loading email failed")
		}

		attachments, err := b.attachmentsRepo.ListByEmail(c.Context(), id)
		if err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, "loading attachments failed")
		}
		attResp := make([]attachmentResponse, len(attachments))
		for i, a := range attachments {
			attResp[i] = attachmentResponse{ID: a.ID, Filename: a.Filename, MIMEType: a.MIMEType, Size: a.Size}
		}

		resp := emailResponse{
			ID: e.ID, MessageID: e.MessageID, Subject: e.Subject, From: e.FromAddr,
			To: e.ToAddrs, Cc: e.CcAddrs, Date: e.Date, AccountID: e.AccountID, FolderID: e.FolderID,
			Size: e.Size, HasAttachments: e.HasAttachments, StorageLocation: e.StorageLocation,
			Attachments: attResp,
		}

		if raw, err := loadEmailContent(c.Context(), b, e); err == nil {
			parts := mimeparse.ParseBodyParts(raw)
			resp.BodyHTML = sanitizeEmailHTML(parts.HTML)
			resp.BodyText = parts.Text
		}

		return c.JSON(resp)
	}
}

// handleDownloadEmail streams the original, unmodified .eml (FR-VW-02),
// lazy-loading from S3 (FR-RS-03) if the email isn't stored locally
// anymore. The local case still uses c.SendFile (a real path the Fiber
// server can stream straight off disk) rather than routing through
// loadEmailContent's in-memory read — no reason to buffer a whole .eml
// just to hand it back to the same process's own response writer.
func handleDownloadEmail(vault *vaultState) fiber.Handler {
	return func(c *fiber.Ctx) error {
		b, err := currentBackendOrLocked(vault)
		if err != nil {
			return err
		}
		id, err := idParam(c, "id")
		if err != nil {
			return err
		}

		e, err := b.emailsRepo.GetByID(c.Context(), id)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fiber.NewError(fiber.StatusNotFound, "email not found")
			}
			return fiber.NewError(fiber.StatusInternalServerError, "loading email failed")
		}

		c.Set(fiber.HeaderContentType, "message/rfc822")
		c.Attachment(fmt.Sprintf("email-%d.eml", e.ID))

		if e.StorageLocation == "local" {
			if e.LocalPath == "" {
				return fiber.NewError(fiber.StatusNotFound, "email is not available locally")
			}
			return c.SendFile(e.LocalPath, false)
		}

		raw, err := loadEmailContent(c.Context(), b, e)
		if err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, "loading email from S3 failed")
		}
		return c.Send(raw)
	}
}

// handleDownloadAttachment streams one attachment's decoded content.
// Attachment content is never stored separately from its parent .eml
// (see mimeparse.ParseAttachments' doc comment), so this re-reads the
// parent file and re-extracts the specific part by position — the
// attachment's index among its email's siblings, in the same order they
// were originally archived in.
func handleDownloadAttachment(vault *vaultState) fiber.Handler {
	return func(c *fiber.Ctx) error {
		b, err := currentBackendOrLocked(vault)
		if err != nil {
			return err
		}
		emailID, err := idParam(c, "id")
		if err != nil {
			return err
		}
		attID, err := idParam(c, "att_id")
		if err != nil {
			return err
		}

		att, err := b.attachmentsRepo.GetByID(c.Context(), attID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fiber.NewError(fiber.StatusNotFound, "attachment not found")
			}
			return fiber.NewError(fiber.StatusInternalServerError, "loading attachment failed")
		}
		if att.EmailID != emailID {
			// The attachment exists, just not under this email — treat it
			// the same as "not found" rather than leaking that the id is
			// valid for a different email.
			return fiber.NewError(fiber.StatusNotFound, "attachment not found")
		}

		e, err := b.emailsRepo.GetByID(c.Context(), emailID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fiber.NewError(fiber.StatusNotFound, "email not found")
			}
			return fiber.NewError(fiber.StatusInternalServerError, "loading email failed")
		}

		siblings, err := b.attachmentsRepo.ListByEmail(c.Context(), emailID)
		if err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, "loading attachments failed")
		}
		index := -1
		for i, s := range siblings {
			if s.ID == attID {
				index = i
				break
			}
		}
		if index == -1 {
			return fiber.NewError(fiber.StatusNotFound, "attachment not found")
		}

		raw, err := loadEmailContent(c.Context(), b, e)
		if err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, "reading archived email failed")
		}
		content, ok := mimeparse.ExtractAttachmentAt(raw, index)
		if !ok {
			return fiber.NewError(fiber.StatusInternalServerError, "extracting attachment content failed")
		}

		mimeType := att.MIMEType
		if mimeType == "" {
			mimeType = "application/octet-stream"
		}
		c.Set(fiber.HeaderContentType, mimeType)
		c.Attachment(att.Filename)
		return c.Send(content)
	}
}

// handleDeleteEmail permanently removes one email from the archive —
// the row (cascading to attachments, s3_upload_queue, and restore_logs),
// the local .eml if one exists, its S3 object if one was uploaded, and
// its search index entry. This is the escape hatch for the case
// internal/sync/fetch.go's Message-ID dedup check guards against going
// forward: an email restored (internal/restore) back into a mailbox
// before that check existed, then re-archived as an unrelated duplicate
// on the next sync — nothing else in the archive can remove it.
//
// An email with an S3 copy that can't currently be reached (S3 not
// configured, or was disabled after the upload happened) is refused
// rather than deleted partially — silently orphaning a paid-for S3
// object with no local record of it left behind is worse than the user
// having to retry once S3 is reachable again.
// errEmailHasS3CopyButS3NotConfigured is deleteEmailCompletely's sentinel
// for the one failure mode that isn't a generic 500 — a caller-visible
// 409 Conflict instead — so handleDeleteEmail/handleBulkDeleteEmails can
// tell it apart from every other kind of delete failure.
var errEmailHasS3CopyButS3NotConfigured = errors.New("email has an S3 copy but S3 is not currently configured — cannot delete it completely")

// deleteEmailCompletely performs the full delete cascade — S3 object (if
// any), the SQL row and everything ON DELETE CASCADE reaches from it,
// the local Maildir file, and the search index entry — shared by the
// single-email and bulk-delete handlers so the two can never drift on
// what "delete" actually does.
func deleteEmailCompletely(ctx context.Context, b *backend, e *domain.Email) error {
	if e.S3Key != "" {
		if b.lazyLoader == nil {
			return errEmailHasS3CopyButS3NotConfigured
		}
		if err := b.lazyLoader.Client.Delete(ctx, e.S3Key); err != nil {
			return fmt.Errorf("deleting S3 object failed: %w", err)
		}
	}

	if err := b.w.Do(ctx, func(tx *sql.Tx) error {
		return b.emailsRepo.DeleteCompletely(ctx, tx, e.ID)
	}); err != nil {
		return fmt.Errorf("deleting email failed: %w", err)
	}

	if e.LocalPath != "" {
		_ = os.Remove(e.LocalPath)
	}
	_ = b.currentIndex().Delete(e.ID)
	return nil
}

func handleDeleteEmail(vault *vaultState) fiber.Handler {
	return func(c *fiber.Ctx) error {
		b, err := currentBackendOrLocked(vault)
		if err != nil {
			return err
		}
		id, err := idParam(c, "id")
		if err != nil {
			return err
		}

		e, err := b.emailsRepo.GetByID(c.Context(), id)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fiber.NewError(fiber.StatusNotFound, "email not found")
			}
			return fiber.NewError(fiber.StatusInternalServerError, "loading email failed")
		}

		if err := deleteEmailCompletely(c.Context(), b, e); err != nil {
			if errors.Is(err, errEmailHasS3CopyButS3NotConfigured) {
				return fiber.NewError(fiber.StatusConflict, err.Error())
			}
			return fiber.NewError(fiber.StatusInternalServerError, err.Error())
		}

		b.audit(domain.AuditEventEmailDelete, c.IP(), fmt.Sprintf("Deleted email #%d (%q) from the archive", e.ID, e.Subject))

		return c.SendStatus(fiber.StatusNoContent)
	}
}

// bulkDeleteRequest mirrors exportRequest's dual-mode selection (P2-17
// "массовые действия над результатами поиска") — an explicit email_ids
// selection, or a search query resolved the same way GET
// /api/v1/search/export do (resolveExportQuery, reused as-is: nothing
// about it is actually export-specific despite the name).
type bulkDeleteRequest struct {
	EmailIDs []int64            `json:"email_ids"`
	Query    *exportQueryFilter `json:"query"`
}

type bulkDeleteResponse struct {
	Deleted int `json:"deleted"`
	Failed  int `json:"failed"`
}

// handleBulkDeleteEmails deletes every email in the selection
// synchronously (unlike bulk restore, deleting is local-only — no IMAP
// round-trip per item — so there's no need for the job-id/WS-progress
// machinery restore's bulk endpoint needs). One email failing (most
// commonly: an S3-resident email when S3 isn't currently configured)
// doesn't abort the rest of the batch; the response reports how many of
// each.
func handleBulkDeleteEmails(vault *vaultState) fiber.Handler {
	return func(c *fiber.Ctx) error {
		b, err := currentBackendOrLocked(vault)
		if err != nil {
			return err
		}
		var req bulkDeleteRequest
		if err := c.BodyParser(&req); err != nil {
			return fiber.NewError(fiber.StatusBadRequest, "invalid request body")
		}

		emailIDs := req.EmailIDs
		if len(emailIDs) == 0 && req.Query != nil {
			ids, err := resolveExportQuery(c.Context(), b, req.Query)
			if err != nil {
				return fiber.NewError(fiber.StatusBadRequest, err.Error())
			}
			emailIDs = ids
		}
		if len(emailIDs) == 0 {
			return fiber.NewError(fiber.StatusBadRequest, "email_ids must not be empty (or query must match at least one email)")
		}

		var resp bulkDeleteResponse
		for _, id := range emailIDs {
			e, err := b.emailsRepo.GetByID(c.Context(), id)
			if err != nil {
				resp.Failed++
				continue
			}
			if err := deleteEmailCompletely(c.Context(), b, e); err != nil {
				resp.Failed++
				continue
			}
			resp.Deleted++
		}

		b.audit(domain.AuditEventEmailDelete, c.IP(), fmt.Sprintf("Bulk delete: %d email(s) deleted, %d failed", resp.Deleted, resp.Failed))

		return c.JSON(resp)
	}
}

// bulkFolderMoveRequest mirrors bulkDeleteRequest's dual-mode selection,
// plus the destination — a plain folder name, same convention as every
// other place in this codebase that names an IMAP folder as a string
// (e.g. restore's target_folder_root).
type bulkFolderMoveRequest struct {
	EmailIDs         []int64            `json:"email_ids"`
	Query            *exportQueryFilter `json:"query"`
	TargetFolderName string             `json:"target_folder_name"`
}

type bulkFolderMoveResponse struct {
	Moved  int `json:"moved"`
	Failed int `json:"failed"`
}

// resolveManualFolder finds-or-creates accountID's folderName folder,
// inside its own Single-Writer transaction — see
// repo.FoldersRepo.GetOrCreateManual's doc comment for why this must never
// go through UpsertFolder.
func resolveManualFolder(ctx context.Context, b *backend, accountID int64, folderName string) (*domain.Folder, error) {
	var target *domain.Folder
	err := b.w.Do(ctx, func(tx *sql.Tx) error {
		f, err := b.foldersRepo.GetOrCreateManual(ctx, tx, accountID, folderName)
		if err != nil {
			return err
		}
		target = f
		return nil
	})
	if err != nil {
		return nil, err
	}
	return target, nil
}

// moveEmailToFolder relocates e into target: the physical .eml file first
// (if e has one locally — an S3-only email has nothing on disk to move),
// then the DB row, mirroring ArchiveOne's own ordering rationale (a file
// rename that succeeds but is never reflected in SQLite is a harmless
// orphan; a DB row committed before the rename, if the process died in
// between, would instead point at a path that's about to stop existing —
// the strictly worse failure mode). If the DB write fails after a
// successful rename, the file is best-effort moved back so a transient DB
// error doesn't leave the row's local_path pointing at a file that's no
// longer there.
func moveEmailToFolder(ctx context.Context, b *backend, e *domain.Email, target *domain.Folder) error {
	newLocalPath := e.LocalPath
	if e.LocalPath != "" {
		dir := maildir.FolderDir(b.maildirRoot, e.AccountID, maildir.SafeFolderName(target.FolderName))
		layout, err := maildir.NewLayout(dir)
		if err != nil {
			return fmt.Errorf("preparing target folder directory: %w", err)
		}
		dest := filepath.Join(layout.New, filepath.Base(e.LocalPath))
		if err := os.Rename(e.LocalPath, dest); err != nil {
			return fmt.Errorf("moving local file: %w", err)
		}
		newLocalPath = dest
	}

	err := b.w.Do(ctx, func(tx *sql.Tx) error {
		uid, err := b.emailsRepo.NextManualMoveUID(ctx, tx, target.ID)
		if err != nil {
			return err
		}
		return b.emailsRepo.UpdateFolderAssignment(ctx, tx, e.ID, target.ID, uid, newLocalPath)
	})
	if err != nil {
		if newLocalPath != e.LocalPath {
			_ = os.Rename(newLocalPath, e.LocalPath)
		}
		return fmt.Errorf("updating email record: %w", err)
	}
	return nil
}

// handleBulkFolderMove moves every email in the selection into
// target_folder_name, same-account only — a folder is scoped to
// account_id, so an email belonging to a different account than the batch's
// first resolved email is counted as failed rather than silently moved
// somewhere unexpected. Like bulk-delete, this is synchronous (a local file
// rename + one DB write per item, no IMAP round-trip), so no job-id/WS
// progress machinery is needed.
func handleBulkFolderMove(vault *vaultState) fiber.Handler {
	return func(c *fiber.Ctx) error {
		b, err := currentBackendOrLocked(vault)
		if err != nil {
			return err
		}
		var req bulkFolderMoveRequest
		if err := c.BodyParser(&req); err != nil {
			return fiber.NewError(fiber.StatusBadRequest, "invalid request body")
		}
		targetFolderName := strings.TrimSpace(req.TargetFolderName)
		if targetFolderName == "" {
			return fiber.NewError(fiber.StatusBadRequest, "target_folder_name must not be empty")
		}

		emailIDs := req.EmailIDs
		if len(emailIDs) == 0 && req.Query != nil {
			ids, err := resolveExportQuery(c.Context(), b, req.Query)
			if err != nil {
				return fiber.NewError(fiber.StatusBadRequest, err.Error())
			}
			emailIDs = ids
		}
		if len(emailIDs) == 0 {
			return fiber.NewError(fiber.StatusBadRequest, "email_ids must not be empty (or query must match at least one email)")
		}

		var resp bulkFolderMoveResponse
		var accountID int64
		var target *domain.Folder

		for _, id := range emailIDs {
			e, err := b.emailsRepo.GetByID(c.Context(), id)
			if err != nil {
				resp.Failed++
				continue
			}

			if target == nil {
				accountID = e.AccountID
				f, err := resolveManualFolder(c.Context(), b, accountID, targetFolderName)
				if err != nil {
					return fiber.NewError(fiber.StatusInternalServerError, "resolving target folder failed")
				}
				target = f
			} else if e.AccountID != accountID {
				resp.Failed++
				continue
			}

			if e.FolderID == target.ID {
				resp.Moved++ // already in the target folder
				continue
			}

			if err := moveEmailToFolder(c.Context(), b, e, target); err != nil {
				resp.Failed++
				continue
			}
			resp.Moved++
		}

		b.audit(domain.AuditEventEmailMove, c.IP(), fmt.Sprintf("Bulk folder move: %d email(s) moved to %q, %d failed", resp.Moved, targetFolderName, resp.Failed))

		return c.JSON(resp)
	}
}
