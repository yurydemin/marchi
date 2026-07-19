package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/yurydemin/marchi/internal/domain"
	"github.com/yurydemin/marchi/internal/s3config"
	"github.com/yurydemin/marchi/internal/s3store"
)

// registerS3Settings wires the S3 Settings API (FR-S3-02, Phase 3 step 9):
// CRUD over the s3_config singleton and a saved-settings connection test,
// mirroring registerAccounts' shape for IMAP accounts.
func registerS3Settings(app *fiber.App, vault *vaultState) {
	app.Get("/api/v1/s3/settings", handleGetS3Settings(vault))
	app.Put("/api/v1/s3/settings", handleSaveS3Settings(vault))
	app.Post("/api/v1/s3/settings/test", handleTestS3Settings(vault))
}

// s3SettingsResponse never includes the encrypted key bytes, the same
// convention accountResponse follows for imap_password_encrypted —
// CredentialsConfigured tells the UI whether keys are set without
// exposing anything derived from them.
type s3SettingsResponse struct {
	Enabled               bool      `json:"enabled"`
	Endpoint              string    `json:"endpoint"`
	Region                string    `json:"region"`
	Bucket                string    `json:"bucket"`
	PathStyle             bool      `json:"path_style"`
	StorageClass          string    `json:"storage_class"`
	TLSSkipVerify         bool      `json:"tls_skip_verify"`
	CredentialsConfigured bool      `json:"credentials_configured"`
	UpdatedAt             time.Time `json:"updated_at"`
}

func handleGetS3Settings(vault *vaultState) fiber.Handler {
	return func(c *fiber.Ctx) error {
		b, err := currentBackendOrLocked(vault)
		if err != nil {
			return err
		}
		s, err := b.s3ConfigManager.Get(c.Context())
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return c.Status(fiber.StatusOK).JSON(fiber.Map{"configured": false})
			}
			return fiber.NewError(fiber.StatusInternalServerError, "loading s3 settings failed")
		}
		resp := s3SettingsResponseFrom(s)
		return c.JSON(fiber.Map{"configured": true, "settings": resp})
	}
}

func s3SettingsResponseFrom(s *domain.S3Settings) s3SettingsResponse {
	return s3SettingsResponse{
		Enabled: s.Enabled, Endpoint: s.Endpoint, Region: s.Region, Bucket: s.Bucket,
		PathStyle: s.PathStyle, StorageClass: s.StorageClass, TLSSkipVerify: s.TLSSkipVerify,
		CredentialsConfigured: len(s.AccessKeyEncrypted) > 0 && len(s.SecretKeyEncrypted) > 0,
		UpdatedAt:             s.UpdatedAt,
	}
}

type saveS3SettingsRequest struct {
	Enabled       bool   `json:"enabled"`
	Endpoint      string `json:"endpoint"`
	Region        string `json:"region"`
	Bucket        string `json:"bucket"`
	AccessKey     string `json:"access_key"` // "" keeps the currently stored key
	SecretKey     string `json:"secret_key"` // "" keeps the currently stored key
	PathStyle     bool   `json:"path_style"`
	StorageClass  string `json:"storage_class"`
	TLSSkipVerify bool   `json:"tls_skip_verify"`
}

func handleSaveS3Settings(vault *vaultState) fiber.Handler {
	return func(c *fiber.Ctx) error {
		b, err := currentBackendOrLocked(vault)
		if err != nil {
			return err
		}
		var req saveS3SettingsRequest
		if err := c.BodyParser(&req); err != nil {
			return fiber.NewError(fiber.StatusBadRequest, "invalid request body")
		}

		s, err := b.s3ConfigManager.Save(c.Context(), s3config.SaveParams{
			Enabled: req.Enabled, Endpoint: req.Endpoint, Region: req.Region, Bucket: req.Bucket,
			AccessKey: req.AccessKey, SecretKey: req.SecretKey, PathStyle: req.PathStyle,
			StorageClass: req.StorageClass, TLSSkipVerify: req.TLSSkipVerify,
		})
		if err != nil {
			return fiber.NewError(fiber.StatusBadRequest, err.Error())
		}
		return c.JSON(s3SettingsResponseFrom(s))
	}
}

// handleTestS3Settings tests the currently saved settings (not an
// unsaved payload) — the same PUT-then-test flow handleTestAccount uses
// for IMAP accounts. It only ever does a HeadBucket-style reachability
// check (s3store.Client.Ping): unlike the MinIO test harness's
// EnsureBucket, this must never silently create a bucket in a user's real
// S3 account as a side effect of "test connection".
func handleTestS3Settings(vault *vaultState) fiber.Handler {
	return func(c *fiber.Ctx) error {
		b, err := currentBackendOrLocked(vault)
		if err != nil {
			return err
		}
		s, err := b.s3ConfigManager.Get(c.Context())
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fiber.NewError(fiber.StatusBadRequest, "s3 is not configured yet")
			}
			return fiber.NewError(fiber.StatusInternalServerError, "loading s3 settings failed")
		}

		accessKey, secretKey, err := b.s3ConfigManager.DecryptCredentials(s)
		if err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, "decrypting s3 credentials failed")
		}

		client, err := s3store.NewClient(s3store.Options{
			Endpoint: s.Endpoint, Region: s.Region, Bucket: s.Bucket,
			AccessKeyID: accessKey, SecretAccessKey: secretKey,
			PathStyle: s.PathStyle, TLSSkipVerify: s.TLSSkipVerify,
		})
		if err != nil {
			return c.Status(fiber.StatusOK).JSON(fiber.Map{"ok": false, "error": err.Error()})
		}

		pingCtx, cancel := context.WithTimeout(c.Context(), 10*time.Second)
		defer cancel()
		if err := client.Ping(pingCtx); err != nil {
			return c.Status(fiber.StatusOK).JSON(fiber.Map{"ok": false, "error": err.Error()})
		}
		return c.JSON(fiber.Map{"ok": true})
	}
}
