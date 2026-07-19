package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/yurydemin/marchi/internal/domain"
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
	TotalEmails       int   `json:"total_emails"`
	TotalAccounts     int   `json:"total_accounts"`
	ActiveAccounts    int   `json:"active_accounts"`
	LocalStorageBytes int64 `json:"local_storage_bytes"`
	S3StorageBytes    int64 `json:"s3_storage_bytes"`
	// S3Configured/S3Enabled reflect whether s3_config exists at all vs.
	// exists-and-Enabled — the Dashboard needs to tell "never set up"
	// apart from "configured but currently turned off" (FR-S3-02).
	S3Configured bool `json:"s3_configured"`
	S3Enabled    bool `json:"s3_enabled"`
	// S3QueuePending/S3QueueUploading/S3QueueFailed are s3_upload_queue's
	// counts by status (FR-S3-06) — Dashboard's "S3 upload queue" tile.
	S3QueuePending   int                    `json:"s3_queue_pending"`
	S3QueueUploading int                    `json:"s3_queue_uploading"`
	S3QueueFailed    int                    `json:"s3_queue_failed"`
	Accounts         []accountStatsResponse `json:"accounts"`
}

func handleStats(vault *vaultState) fiber.Handler {
	return func(c *fiber.Ctx) error {
		b, err := currentBackendOrLocked(vault)
		if err != nil {
			return err
		}
		resp, err := computeStats(c.Context(), b)
		if err != nil {
			return err
		}
		return c.JSON(resp)
	}
}

// computeStats is shared by the JSON API (handleStats) and the Dashboard
// page (pages.go), so the two never drift apart on what a "last sync" or
// a storage total means.
func computeStats(ctx context.Context, b *backend) (statsResponse, error) {
	emailStats, err := b.emailsRepo.Stats(ctx)
	if err != nil {
		return statsResponse{}, fiber.NewError(fiber.StatusInternalServerError, "computing email stats failed")
	}
	accounts, err := b.accountsRepo.List(ctx)
	if err != nil {
		return statsResponse{}, fiber.NewError(fiber.StatusInternalServerError, "listing accounts failed")
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
		if logs, err := b.syncLogsRepo.ListByAccount(ctx, a.ID, 1); err == nil && len(logs) > 0 {
			as.LastSyncStatus = string(logs[0].Status)
			as.LastSyncAt = &logs[0].StartedAt
		}
		resp.Accounts[i] = as
	}

	if s3cfg, err := b.s3ConfigManager.Get(ctx); err == nil {
		resp.S3Configured = true
		resp.S3Enabled = s3cfg.Enabled
	} else if !errors.Is(err, sql.ErrNoRows) {
		return statsResponse{}, fiber.NewError(fiber.StatusInternalServerError, "loading s3 config failed")
	}

	if counts, err := b.s3UploadQueueRepo.CountByStatus(ctx); err == nil {
		resp.S3QueuePending = counts[domain.S3QueueStatusPending]
		resp.S3QueueUploading = counts[domain.S3QueueStatusUploading]
		resp.S3QueueFailed = counts[domain.S3QueueStatusFailed]
	} else {
		return statsResponse{}, fiber.NewError(fiber.StatusInternalServerError, "loading s3 queue status failed")
	}

	return resp, nil
}
