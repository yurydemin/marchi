package httpapi

import (
	"database/sql"
	"errors"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// registerRestoreAPI wires the Restore Engine's HTTP surface (FR-RS-01):
// a bulk (or single-item) restore trigger with progress over the
// existing /ws WebSocket, plus a per-email restore history view
// (FR-RS-04).
func registerRestoreAPI(app *fiber.App, vault *vaultState) {
	app.Post("/api/v1/restore", handleRestore(vault))
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
