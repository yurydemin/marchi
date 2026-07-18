package httpapi

import (
	"strconv"
	"time"

	"github.com/gofiber/fiber/v2"
)

// syncLogsDefaultLimit/MaxLimit mirror the search API's pagination
// defaults in spirit (FR-API-02 doesn't specify numbers for this
// endpoint the way FR-SR-03 does for search) — a small sane default with
// a cap against an accidentally huge request.
const (
	syncLogsDefaultLimit = 20
	syncLogsMaxLimit     = 200
)

// registerLogs wires GET /api/v1/logs/sync (FR-API-02: "журналы
// синхронизации, пагинация") — every account's sync history in one feed,
// as opposed to GET /api/v1/accounts/{id}/sync-status's per-account view
// (Phase 2 step 7).
func registerLogs(app *fiber.App, vault *vaultState) {
	app.Get("/api/v1/logs/sync", handleSyncLogs(vault))
}

type syncLogEntryResponse struct {
	ID              int64     `json:"id"`
	AccountID       int64     `json:"account_id"`
	Email           string    `json:"email,omitempty"`
	StartedAt       time.Time `json:"started_at"`
	EndedAt         time.Time `json:"ended_at,omitempty"`
	EmailsProcessed int       `json:"emails_processed"`
	EmailsArchived  int       `json:"emails_archived"`
	BytesDownloaded int64     `json:"bytes_downloaded"`
	Errors          int       `json:"errors"`
	Status          string    `json:"status"`
	ErrorMsg        string    `json:"error_msg,omitempty"`
}

type syncLogsResponse struct {
	Total  int                    `json:"total"`
	Offset int                    `json:"offset"`
	Limit  int                    `json:"limit"`
	Logs   []syncLogEntryResponse `json:"logs"`
}

func handleSyncLogs(vault *vaultState) fiber.Handler {
	return func(c *fiber.Ctx) error {
		b, err := currentBackendOrLocked(vault)
		if err != nil {
			return err
		}

		offset, limit, err := parseOffsetLimit(c, syncLogsDefaultLimit, syncLogsMaxLimit)
		if err != nil {
			return err
		}

		total, err := b.syncLogsRepo.CountAll(c.Context())
		if err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, "counting sync logs failed")
		}
		logs, err := b.syncLogsRepo.ListRecentPage(c.Context(), offset, limit)
		if err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, "listing sync logs failed")
		}

		accounts, err := b.accountsRepo.List(c.Context())
		if err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, "listing accounts failed")
		}
		emailByAccountID := make(map[int64]string, len(accounts))
		for _, a := range accounts {
			emailByAccountID[a.ID] = a.Email
		}

		resp := syncLogsResponse{Total: total, Offset: offset, Limit: limit, Logs: make([]syncLogEntryResponse, len(logs))}
		for i, l := range logs {
			resp.Logs[i] = syncLogEntryResponse{
				ID: l.ID, AccountID: l.AccountID, Email: emailByAccountID[l.AccountID],
				StartedAt: l.StartedAt, EndedAt: l.EndedAt,
				EmailsProcessed: l.EmailsProcessed, EmailsArchived: l.EmailsArchived,
				BytesDownloaded: l.BytesDownloaded, Errors: l.Errors,
				Status: string(l.Status), ErrorMsg: l.ErrorMsg,
			}
		}
		return c.JSON(resp)
	}
}

// parseOffsetLimit reads the standard "offset"/"limit" query params,
// applying defaultLimit when limit is omitted and capping it at maxLimit
// — shared by any paginated list endpoint that isn't the search API
// (which has its own Params-based parsing already).
func parseOffsetLimit(c *fiber.Ctx, defaultLimit, maxLimit int) (offset, limit int, err error) {
	limit = defaultLimit
	if v := c.Query("limit"); v != "" {
		if limit, err = strconv.Atoi(v); err != nil {
			return 0, 0, fiber.NewError(fiber.StatusBadRequest, "invalid limit")
		}
	}
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	if v := c.Query("offset"); v != "" {
		if offset, err = strconv.Atoi(v); err != nil {
			return 0, 0, fiber.NewError(fiber.StatusBadRequest, "invalid offset")
		}
	}
	if offset < 0 {
		offset = 0
	}
	return offset, limit, nil
}
