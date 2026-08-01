package httpapi

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/yurydemin/marchi/internal/domain"
)

// registerRestoreAPI wires the Restore Engine's HTTP surface (FR-RS-01):
// a bulk-by-selection (or single-item) restore trigger, a bulk-by-scope
// (whole account or whole folder) restore trigger, both with progress
// over the existing /ws WebSocket, plus a per-email restore history view
// (FR-RS-04).
func registerRestoreAPI(app *fiber.App, vault *vaultState) {
	app.Post("/api/v1/restore", handleRestore(vault))
	app.Post("/api/v1/restore/bulk", handleBulkRestore(vault))
	app.Get("/api/v1/emails/:id/restore-logs", handleEmailRestoreLogs(vault))
}

type restoreRequest struct {
	EmailIDs        []int64 `json:"email_ids"`
	TargetAccountID int64   `json:"target_account_id"`
	TargetFolder    string  `json:"target_folder"`
}

// handleRestore validates the request synchronously (unknown target
// account, empty selection) so an obviously-bad request gets a proper
// HTTP error, then runs the actual restore(s) in a tracked background
// job (backend.runRestoreAsync) and returns a job id immediately —
// mirroring handleSyncAccount's and handleAdminReindex's shape. Progress
// and completion go out over /ws under that job id (FR-API-03).
func handleRestore(vault *vaultState) fiber.Handler {
	return func(c *fiber.Ctx) error {
		b, err := currentBackendOrLocked(vault)
		if err != nil {
			return err
		}
		var req restoreRequest
		if err := c.BodyParser(&req); err != nil {
			return fiber.NewError(fiber.StatusBadRequest, "invalid request body")
		}
		if len(req.EmailIDs) == 0 {
			return fiber.NewError(fiber.StatusBadRequest, "email_ids must not be empty")
		}
		if req.TargetFolder == "" {
			return fiber.NewError(fiber.StatusBadRequest, "target_folder is required")
		}
		if _, err := b.accountsRepo.GetByID(c.Context(), req.TargetAccountID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fiber.NewError(fiber.StatusNotFound, "target account not found")
			}
			return fiber.NewError(fiber.StatusInternalServerError, "loading target account failed")
		}

		jobID := uuid.NewString()
		b.runRestoreAsync(jobID, req.EmailIDs, req.TargetAccountID, req.TargetFolder)
		b.audit(domain.AuditEventRestore, c.IP(), fmt.Sprintf("Restore triggered: %d email(s) to account #%d, folder %q (job %s)",
			len(req.EmailIDs), req.TargetAccountID, req.TargetFolder, jobID))
		return c.Status(fiber.StatusAccepted).JSON(fiber.Map{"job_id": jobID})
	}
}

type bulkRestoreRequest struct {
	Scope            string `json:"scope"` // "account" or "folder"
	ScopeID          int64  `json:"scope_id"`
	TargetAccountID  int64  `json:"target_account_id"`
	TargetFolderRoot string `json:"target_folder_root"`
	// DateFrom/DateTo optionally narrow scope's emails by Date before
	// restoring — same RFC3339-or-YYYY-MM-DD format as GET /api/v1/search,
	// both inclusive. Empty means unbounded on that side.
	DateFrom string `json:"date_from,omitempty"`
	DateTo   string `json:"date_to,omitempty"`
}

// parseOptionalDate parses v (if non-empty) with parseSearchDate, or
// returns the zero time for an empty v — the caller's "unbounded" case.
func parseOptionalDate(v, name string) (time.Time, error) {
	if v == "" {
		return time.Time{}, nil
	}
	t, err := parseSearchDate(v)
	if err != nil {
		return time.Time{}, badDateParam(name, v)
	}
	return t, nil
}

// emailDateInRange reports whether date falls within [from, to], treating a
// zero from/to as unbounded on that side — mirrors internal/search's own
// DateRangeInclusiveQuery zero-handling (internal/search/query.go), so a
// date-filtered bulk restore agrees with what a date-filtered search would
// have matched.
func emailDateInRange(date, from, to time.Time) bool {
	if !from.IsZero() && date.Before(from) {
		return false
	}
	if !to.IsZero() && date.After(to) {
		return false
	}
	return true
}

// joinRestoreFolder places sourceFolderName under root — used for a
// whole-account restore, where each source folder needs its own target
// subfolder rather than all emails landing flat in one folder. Doesn't
// attempt to translate delimiters (e.g. "." vs "/"): root and
// sourceFolderName are joined with "/", the delimiter the rest of this
// project's restore UI already assumes when a user types a target
// folder path by hand.
func joinRestoreFolder(root, sourceFolderName string) string {
	return strings.TrimRight(root, "/") + "/" + sourceFolderName
}

// handleBulkRestore restores every email in an entire source account or
// a single source folder into targetAccountID, without the caller having
// to search for and select them individually first (FR-RS-01's "restore
// a whole mailbox/folder" case). For scope="folder", every email lands
// directly in target_folder_root. For scope="account", emails are
// grouped by their source folder and each group gets its own subfolder
// under target_folder_root (via joinRestoreFolder) — so restoring a
// whole mailbox doesn't flatten its folder structure into one pile.
func handleBulkRestore(vault *vaultState) fiber.Handler {
	return func(c *fiber.Ctx) error {
		b, err := currentBackendOrLocked(vault)
		if err != nil {
			return err
		}
		var req bulkRestoreRequest
		if err := c.BodyParser(&req); err != nil {
			return fiber.NewError(fiber.StatusBadRequest, "invalid request body")
		}
		if req.TargetFolderRoot == "" {
			return fiber.NewError(fiber.StatusBadRequest, "target_folder_root is required")
		}
		if _, err := b.accountsRepo.GetByID(c.Context(), req.TargetAccountID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fiber.NewError(fiber.StatusNotFound, "target account not found")
			}
			return fiber.NewError(fiber.StatusInternalServerError, "loading target account failed")
		}
		dateFrom, err := parseOptionalDate(req.DateFrom, "date_from")
		if err != nil {
			return fiber.NewError(fiber.StatusBadRequest, err.Error())
		}
		dateTo, err := parseOptionalDate(req.DateTo, "date_to")
		if err != nil {
			return fiber.NewError(fiber.StatusBadRequest, err.Error())
		}

		var items []restoreItem
		switch req.Scope {
		case "folder":
			folder, err := b.foldersRepo.GetByID(c.Context(), req.ScopeID)
			if err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return fiber.NewError(fiber.StatusNotFound, "source folder not found")
				}
				return fiber.NewError(fiber.StatusInternalServerError, "loading source folder failed")
			}
			emails, err := b.emailsRepo.ListByFolder(c.Context(), folder.ID)
			if err != nil {
				return fiber.NewError(fiber.StatusInternalServerError, "listing folder emails failed")
			}
			items = make([]restoreItem, 0, len(emails))
			for _, e := range emails {
				if !emailDateInRange(e.Date, dateFrom, dateTo) {
					continue
				}
				items = append(items, restoreItem{EmailID: e.ID, TargetFolder: req.TargetFolderRoot})
			}
		case "account":
			if _, err := b.accountsRepo.GetByID(c.Context(), req.ScopeID); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return fiber.NewError(fiber.StatusNotFound, "source account not found")
				}
				return fiber.NewError(fiber.StatusInternalServerError, "loading source account failed")
			}
			emails, err := b.emailsRepo.ListByAccount(c.Context(), req.ScopeID)
			if err != nil {
				return fiber.NewError(fiber.StatusInternalServerError, "listing account emails failed")
			}
			folderTargets := make(map[int64]string, len(emails))
			items = make([]restoreItem, 0, len(emails))
			for _, e := range emails {
				if !emailDateInRange(e.Date, dateFrom, dateTo) {
					continue
				}
				target, ok := folderTargets[e.FolderID]
				if !ok {
					folder, err := b.foldersRepo.GetByID(c.Context(), e.FolderID)
					if err != nil {
						return fiber.NewError(fiber.StatusInternalServerError, "loading source folder failed")
					}
					target = joinRestoreFolder(req.TargetFolderRoot, folder.FolderName)
					folderTargets[e.FolderID] = target
				}
				items = append(items, restoreItem{EmailID: e.ID, TargetFolder: target})
			}
		default:
			return fiber.NewError(fiber.StatusBadRequest, `scope must be "account" or "folder"`)
		}

		if len(items) == 0 {
			return fiber.NewError(fiber.StatusBadRequest, "nothing to restore")
		}

		jobID := uuid.NewString()
		b.runRestoreItemsAsync(jobID, items, req.TargetAccountID)
		b.audit(domain.AuditEventRestore, c.IP(), fmt.Sprintf("Bulk restore triggered: scope=%s scope_id=%d, %d email(s) to account #%d under %q (job %s)",
			req.Scope, req.ScopeID, len(items), req.TargetAccountID, req.TargetFolderRoot, jobID))
		return c.Status(fiber.StatusAccepted).JSON(fiber.Map{"job_id": jobID})
	}
}

type restoreLogResponse struct {
	ID              int64     `json:"id"`
	EmailID         int64     `json:"email_id"`
	TargetAccountID int64     `json:"target_account_id"`
	TargetFolder    string    `json:"target_folder"`
	Method          string    `json:"method"`
	Status          string    `json:"status"`
	ErrorMsg        string    `json:"error_msg,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
}

// handleEmailRestoreLogs returns emailID's restore history, newest
// first — the durable record FR-RS-04 asks for, independent of whatever
// a client did or didn't see over /ws while a restore was running.
func handleEmailRestoreLogs(vault *vaultState) fiber.Handler {
	return func(c *fiber.Ctx) error {
		b, err := currentBackendOrLocked(vault)
		if err != nil {
			return err
		}
		id, err := idParam(c, "id")
		if err != nil {
			return err
		}
		logs, err := b.restoreLogsRepo.ListByEmail(c.Context(), id)
		if err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, "loading restore history failed")
		}
		resp := make([]restoreLogResponse, len(logs))
		for i, l := range logs {
			resp[i] = restoreLogResponse{
				ID: l.ID, EmailID: l.EmailID, TargetAccountID: l.TargetAccountID, TargetFolder: l.TargetFolder,
				Method: l.Method, Status: l.Status, ErrorMsg: l.ErrorMsg, CreatedAt: l.CreatedAt,
			}
		}
		return c.JSON(resp)
	}
}
