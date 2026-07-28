package httpapi

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/yurydemin/marchi/internal/domain"
	"github.com/yurydemin/marchi/internal/mimeparse"
)

// registerEmails wires the Emails REST API (FR-API-02, FR-VW-01/02):
// metadata + sanitized body preview, downloading the original .eml,
// downloading an individual attachment, and deleting a single email.
func registerEmails(app *fiber.App, vault *vaultState) {
	app.Get("/api/v1/emails/:id", handleGetEmail(vault))
	app.Get("/api/v1/emails/:id/download", handleDownloadEmail(vault))
	app.Get("/api/v1/emails/:id/attachments/:att_id/download", handleDownloadAttachment(vault))
	app.Delete("/api/v1/emails/:id", handleDeleteEmail(vault))
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

		if e.S3Key != "" {
			if b.lazyLoader == nil {
				return fiber.NewError(fiber.StatusConflict, "email has an S3 copy but S3 is not currently configured — cannot delete it completely")
			}
			if err := b.lazyLoader.Client.Delete(c.Context(), e.S3Key); err != nil {
				return fiber.NewError(fiber.StatusInternalServerError, "deleting S3 object failed")
			}
		}

		if err := b.w.Do(c.Context(), func(tx *sql.Tx) error {
			return b.emailsRepo.DeleteCompletely(c.Context(), tx, e.ID)
		}); err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, "deleting email failed")
		}

		if e.LocalPath != "" {
			_ = os.Remove(e.LocalPath)
		}
		_ = b.currentIndex().Delete(e.ID)

		b.audit(domain.AuditEventEmailDelete, c.IP(), fmt.Sprintf("Deleted email #%d (%q) from the archive", e.ID, e.Subject))

		return c.SendStatus(fiber.StatusNoContent)
	}
}
