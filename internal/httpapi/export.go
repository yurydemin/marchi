package httpapi

import (
	"archive/zip"
	"bufio"
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/yurydemin/marchi/internal/domain"
	"github.com/yurydemin/marchi/internal/search"
)

// registerExport wires the bulk .zip export endpoint (FR-EX-01, Phase 3
// step 15): an explicit email_ids selection, or a search query resolved
// the same way GET /api/v1/search does. Unlike restore/reindex's
// trigger-then-poll-a-job-id shape, the response body here IS the
// download — there's no separate "fetch the result" step — so this is a
// synchronous streamed response, not a backgrounded job. Progress still
// goes out over the existing /ws feed under a job id (FR-API-03), purely
// as an extra signal a UI can use for a progress bar alongside the
// browser's own native download progress.
func registerExport(app *fiber.App, vault *vaultState) {
	app.Post("/api/v1/export", handleExport(vault))
}

type exportRequest struct {
	EmailIDs []int64 `json:"email_ids"`
	// Query, if EmailIDs is empty, resolves the export's email set via
	// the search index instead of an explicit selection ("результат
	// поиска" from the plan) — capped at exportSearchMaxResults.
	Query *exportQueryFilter `json:"query"`
	// JobID lets the caller pick its own id (so it can subscribe to /ws
	// before the download request is even sent, avoiding any race with
	// the first progress event). A server-generated uuid is used if
	// omitted.
	JobID string `json:"job_id"`
}

// exportQueryFilter mirrors parseSearchParams' query-string fields as a
// JSON body — export is a POST whose response body is the .zip itself,
// so search criteria travel in the request body instead of a query
// string.
type exportQueryFilter struct {
	Q              string `json:"q"`
	Sender         string `json:"sender"`
	Recipient      string `json:"recipient"`
	From           string `json:"from"` // RFC3339 or YYYY-MM-DD, like GET /api/v1/search
	To             string `json:"to"`
	AccountID      int64  `json:"account_id"`
	FolderID       int64  `json:"folder_id"`
	HasAttachments *bool  `json:"has_attachments"`
}

// exportSearchPageSize/exportSearchMaxResults bound a query-based
// export: paged through the index exportSearchPageSize hits at a time,
// up to exportSearchMaxResults total — a safety cap against an
// open-ended query accidentally streaming the entire archive in one
// request.
const (
	exportSearchPageSize   = 200
	exportSearchMaxResults = 5000
)

func handleExport(vault *vaultState) fiber.Handler {
	return func(c *fiber.Ctx) error {
		b, err := currentBackendOrLocked(vault)
		if err != nil {
			return err
		}
		var req exportRequest
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

		jobID := req.JobID
		if jobID == "" {
			jobID = uuid.NewString()
		}

		c.Set(fiber.HeaderContentType, "application/zip")
		c.Set(fiber.HeaderContentDisposition, fmt.Sprintf(`attachment; filename="mailvault-export-%s.zip"`, time.Now().Format("20060102-150405")))
		c.Set("X-Job-Id", jobID)

		// context.Background(), not c.Context(): the stream writer below
		// runs after this handler returns (that's the whole point of
		// SetBodyStreamWriter), so it needs a context that outlives the
		// handler call rather than one tied to fasthttp's per-request
		// lifecycle.
		ctx := context.Background()
		c.Context().SetBodyStreamWriter(func(w *bufio.Writer) {
			b.streamExport(ctx, w, jobID, emailIDs)
		})
		return nil
	}
}

// resolveExportQuery pages through the search index collecting every
// matching email id, the same Params shape GET /api/v1/search's
// parseSearchParams builds from query-string values.
func resolveExportQuery(ctx context.Context, b *backend, q *exportQueryFilter) ([]int64, error) {
	params := search.Params{
		Query: q.Q, Sender: q.Sender, Recipient: q.Recipient,
		AccountID: q.AccountID, FolderID: q.FolderID, HasAttachments: q.HasAttachments,
		Sort: search.SortDateDesc, Limit: exportSearchPageSize,
	}
	if q.From != "" {
		t, err := parseSearchDate(q.From)
		if err != nil {
			return nil, badDateParam("from", q.From)
		}
		params.DateFrom = t
	}
	if q.To != "" {
		t, err := parseSearchDate(q.To)
		if err != nil {
			return nil, badDateParam("to", q.To)
		}
		params.DateTo = t
	}

	var ids []int64
	for {
		result, err := b.currentIndex().Search(ctx, params)
		if err != nil {
			return nil, fmt.Errorf("resolving search query: %w", err)
		}
		for _, h := range result.Hits {
			ids = append(ids, h.EmailID)
		}
		if len(result.Hits) < params.Limit || len(ids) >= exportSearchMaxResults {
			break
		}
		params.Offset += params.Limit
	}
	if len(ids) > exportSearchMaxResults {
		ids = ids[:exportSearchMaxResults]
	}
	return ids, nil
}

// streamExport writes a .zip of emailIDs' original .eml content directly
// to w as each one is read, broadcasting progress over jobID after every
// attempt. One email's failure (bad id, S3 not configured, a read/write
// error) is logged into the running error count and skipped rather than
// aborting the whole export — the same "don't let one bad item sink the
// batch" philosophy backend.runRestoreAsync follows. Account/folder
// lookups are cached per export since a batch is typically dominated by
// a handful of account/folder combinations repeated across many emails.
func (b *backend) streamExport(ctx context.Context, w *bufio.Writer, jobID string, emailIDs []int64) {
	total := len(emailIDs)
	processed, errCount := 0, 0
	zw := zip.NewWriter(w)

	accountCache := map[int64]*domain.Account{}
	folderCache := map[int64]*domain.Folder{}

	fail := func(id int64, err error) {
		errCount++
		b.wsHub.broadcast(exportWSEvent(jobID, processed, total, errCount, false, fmt.Sprintf("email %d: %v", id, err)))
	}

	for _, id := range emailIDs {
		processed++

		e, err := b.emailsRepo.GetByID(ctx, id)
		if err != nil {
			fail(id, fmt.Errorf("loading email: %w", err))
			continue
		}
		content, err := b.loadEmailContent(ctx, e)
		if err != nil {
			fail(id, err)
			continue
		}

		acct, ok := accountCache[e.AccountID]
		if !ok {
			acct, err = b.accountsRepo.GetByID(ctx, e.AccountID)
			if err != nil {
				fail(id, fmt.Errorf("loading account: %w", err))
				continue
			}
			accountCache[e.AccountID] = acct
		}
		folder, ok := folderCache[e.FolderID]
		if !ok {
			folder, err = b.foldersRepo.GetByID(ctx, e.FolderID)
			if err != nil {
				fail(id, fmt.Errorf("loading folder: %w", err))
				continue
			}
			folderCache[e.FolderID] = folder
		}

		fw, err := zw.Create(exportEntryName(acct.Email, folder.FolderName, e))
		if err == nil {
			_, err = fw.Write(content)
		}
		if err != nil {
			fail(id, fmt.Errorf("writing zip entry: %w", err))
			continue
		}
		_ = w.Flush()
		b.wsHub.broadcast(exportWSEvent(jobID, processed, total, errCount, false, ""))
	}

	_ = zw.Close()
	_ = w.Flush()
	b.wsHub.broadcast(exportWSEvent(jobID, total, total, errCount, true, ""))
}

// loadEmailContent returns e's raw .eml bytes, lazily loading and
// decrypting from S3 (FR-RS-03) if it's no longer stored locally — the
// same fallback restore.Restorer.loadContent implements for the Restore
// Engine. Duplicated narrowly here rather than exported from that
// package: restore.Restorer.loadContent is otherwise entirely
// restore-specific (recording restore_logs, choosing between APPEND and
// SMTP), not a general-purpose library function worth extracting for one
// other caller.
func (b *backend) loadEmailContent(ctx context.Context, e *domain.Email) ([]byte, error) {
	if e.StorageLocation == "local" {
		content, err := os.ReadFile(e.LocalPath)
		if err != nil {
			return nil, fmt.Errorf("reading local .eml: %w", err)
		}
		return content, nil
	}
	if b.lazyLoader == nil {
		return nil, fmt.Errorf("email is stored in s3 but s3 is not configured, can't lazy-load it")
	}
	content, err := b.lazyLoader.Load(ctx, e.S3Key)
	if err != nil {
		return nil, fmt.Errorf("lazy-loading from s3: %w", err)
	}
	return content, nil
}

// exportEntryName builds one zip entry's path per the plan's layout:
// {account_email}/{folder}/{YYYY-MM}/{Subject}-{MessageID}.eml. Every
// component is sanitized independently — Subject and MessageID are
// sender-controlled free text and must never be allowed to smuggle a "/"
// (or anything else path-like) into the zip's directory structure, which
// archive/zip itself does nothing to prevent.
func exportEntryName(accountEmail, folderName string, e *domain.Email) string {
	subject := strings.TrimSpace(e.Subject)
	if subject == "" {
		subject = "no-subject"
	}
	messageID := strings.TrimSpace(e.MessageID)
	if messageID == "" {
		messageID = fmt.Sprintf("id-%d", e.ID)
	}
	return fmt.Sprintf("%s/%s/%s/%s-%s.eml",
		sanitizeExportComponent(accountEmail),
		sanitizeExportComponent(folderName),
		e.Date.Format("2006-01"),
		sanitizeExportComponent(truncateRunes(subject, 80)),
		sanitizeExportComponent(truncateRunes(messageID, 60)),
	)
}

var exportUnsafeChars = regexp.MustCompile(`[/\\:*?"<>|\x00-\x1f]`)

// sanitizeExportComponent replaces every character that's unsafe in a
// zip entry path segment (path separators, Windows-reserved characters,
// control characters) with "_", and neutralizes ".." so no segment can
// read as a directory-traversal attempt — defense in depth on top of
// archive/zip readers already being expected to reject that themselves.
func sanitizeExportComponent(s string) string {
	s = exportUnsafeChars.ReplaceAllString(s, "_")
	s = strings.ReplaceAll(s, "..", "_")
	s = strings.TrimSpace(s)
	if s == "" {
		return "_"
	}
	return s
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}
