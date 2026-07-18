package httpapi

import (
	"time"

	"github.com/gofiber/fiber/v2"
)

// registerStats wires GET /api/v1/stats (FR-API-02), the archive-wide
// summary the Dashboard (FR-WU-02) is built on.
func registerStats(app *fiber.App, vault *vaultState) {
	app.Get("/api/v1/stats", handleStats(vault))
}

type accountStatsResponse struct {
	AccountID      int64      `json:"account_id"`
	Email          string     `json:"email"`
	IsActive       bool       `json:"is_active"`
	EmailCount     int        `json:"email_count"`
	LastSyncStatus string     `json:"last_sync_status,omitempty"`
	LastSyncAt     *time.Time `json:"last_sync_at,omitempty"`
}

type statsResponse struct {
	TotalEmails       int                    `json:"total_emails"`
	TotalAccounts     int                    `json:"total_accounts"`
	ActiveAccounts    int                    `json:"active_accounts"`
	LocalStorageBytes int64                  `json:"local_storage_bytes"`
	S3StorageBytes    int64                  `json:"s3_storage_bytes"` // always 0 until Phase 3
	S3QueueSize       int                    `json:"s3_queue_size"`    // always 0 until Phase 3
	Accounts          []accountStatsResponse `json:"accounts"`
}

func handleStats(vault *vaultState) fiber.Handler {
	return func(c *fiber.Ctx) error {
		b, err := currentBackendOrLocked(vault)
		if err != nil {
			return err
		}

		emailStats, err := b.emailsRepo.Stats(c.Context())
		if err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, "computing email stats failed")
		}
		accounts, err := b.accountsRepo.List(c.Context())
		if err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, "listing accounts failed")
		}

		resp := statsResponse{
			TotalEmails:       emailStats.Total,
			TotalAccounts:     len(accounts),
			LocalStorageBytes: emailStats.LocalBytes,
			S3StorageBytes:    emailStats.S3Bytes,
			Accounts:          make([]accountStatsResponse, len(accounts)),
		}
		for i, a := range accounts {
			if a.IsActive {
				resp.ActiveAccounts++
			}
			as := accountStatsResponse{
				AccountID:  a.ID,
				Email:      a.Email,
				IsActive:   a.IsActive,
				EmailCount: emailStats.EmailsByAccount[a.ID],
			}
			// One small query per account for its last sync run — fine at
			// the scale FR-AM-03 describes ("лимит — ресурсы железа"),
			// and avoids a more complex single query for what's normally
			// a handful of accounts.
			if logs, err := b.syncLogsRepo.ListByAccount(c.Context(), a.ID, 1); err == nil && len(logs) > 0 {
				as.LastSyncStatus = string(logs[0].Status)
				as.LastSyncAt = &logs[0].StartedAt
			}
			resp.Accounts[i] = as
		}

		return c.JSON(resp)
	}
}
