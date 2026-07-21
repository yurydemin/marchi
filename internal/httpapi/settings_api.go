package httpapi

import (
	"database/sql"
	"errors"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/yurydemin/marchi/internal/config"
	"github.com/yurydemin/marchi/internal/domain"
	"github.com/yurydemin/marchi/internal/security/masterkey"
)

// registerSettingsAPI wires the two pieces of Settings (Phase 3 step 17)
// that are plain JSON REST endpoints rather than the page or its htmx
// fragments: the global retention defaults (FR-RE-04) and Master Key
// rotation. Both sit under /api/v1, so newLockGate already requires an
// authenticated session before either is reachable.
func registerSettingsAPI(app *fiber.App, cfg *config.Config, vault *vaultState) {
	app.Get("/api/v1/retention/settings", handleGetRetentionSettings(vault))
	app.Put("/api/v1/retention/settings", handleSaveRetentionSettings(vault))
	app.Post("/api/v1/master-key/change", handleChangeMasterKey(cfg))
}

// retentionSettingsResponse mirrors s3SettingsResponse's shape for the
// same reason: a small, stable JSON contract independent of the domain
// struct's own field order. A nil *int means "unlimited" (never runs for
// that stage) unless an account overrides it — see
// internal/retention's package doc.
type retentionSettingsResponse struct {
	DefaultLocalDays    *int      `json:"default_local_days"`
	DefaultMoveToS3Days *int      `json:"default_move_to_s3_days"`
	DefaultS3Days       *int      `json:"default_s3_days"`
	UpdatedAt           time.Time `json:"updated_at"`
}

func retentionSettingsResponseFrom(s *domain.RetentionSettings) retentionSettingsResponse {
	return retentionSettingsResponse{
		DefaultLocalDays: s.DefaultLocalDays, DefaultMoveToS3Days: s.DefaultMoveToS3Days,
		DefaultS3Days: s.DefaultS3Days, UpdatedAt: s.UpdatedAt,
	}
}

func handleGetRetentionSettings(vault *vaultState) fiber.Handler {
	return func(c *fiber.Ctx) error {
		b, err := currentBackendOrLocked(vault)
		if err != nil {
			return err
		}
		s, err := b.retentionSettingsRepo.Get(c.Context())
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return c.Status(fiber.StatusOK).JSON(fiber.Map{"configured": false})
			}
			return fiber.NewError(fiber.StatusInternalServerError, "loading retention settings failed")
		}
		return c.JSON(fiber.Map{"configured": true, "settings": retentionSettingsResponseFrom(s)})
	}
}

type saveRetentionSettingsRequest struct {
	DefaultLocalDays    *int `json:"default_local_days"`
	DefaultMoveToS3Days *int `json:"default_move_to_s3_days"`
	DefaultS3Days       *int `json:"default_s3_days"`
}

func handleSaveRetentionSettings(vault *vaultState) fiber.Handler {
	return func(c *fiber.Ctx) error {
		b, err := currentBackendOrLocked(vault)
		if err != nil {
			return err
		}
		var req saveRetentionSettingsRequest
		if err := c.BodyParser(&req); err != nil {
			return fiber.NewError(fiber.StatusBadRequest, "invalid request body")
		}
		s := &domain.RetentionSettings{
			DefaultLocalDays: req.DefaultLocalDays, DefaultMoveToS3Days: req.DefaultMoveToS3Days,
			DefaultS3Days: req.DefaultS3Days,
		}
		if err := b.retentionSettingsRepo.Upsert(c.Context(), s); err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, "saving retention settings failed")
		}
		saved, err := b.retentionSettingsRepo.Get(c.Context())
		if err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, "loading saved retention settings failed")
		}
		return c.JSON(retentionSettingsResponseFrom(saved))
	}
}

// changeMasterKeyRequest is Settings' password-rotation form. Nothing
// about this needs vaultState at all: masterkey.ChangePassword is a pure
// filesystem operation (see its own doc comment for why re-wrapping the
// DEK is all it has to do), and the process-wide DEK already cached in
// memory (vaultState.dek) stays valid across the rotation unchanged, so
// there's nothing in-memory to update either.
type changeMasterKeyRequest struct {
	CurrentPassword string `json:"current_password" form:"current_password"`
	NewPassword     string `json:"new_password" form:"new_password"`
	ConfirmPassword string `json:"confirm_password" form:"confirm_password"`
}

func handleChangeMasterKey(cfg *config.Config) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var req changeMasterKeyRequest
		if err := c.BodyParser(&req); err != nil {
			return fiber.NewError(fiber.StatusBadRequest, "invalid request body")
		}
		if req.NewPassword != req.ConfirmPassword {
			return fiber.NewError(fiber.StatusBadRequest, "new password and confirmation do not match")
		}

		params := masterkey.Argon2Params{
			Memory:      cfg.Security.Argon2.Memory,
			Iterations:  cfg.Security.Argon2.Iterations,
			Parallelism: cfg.Security.Argon2.Parallelism,
		}
		err := masterkey.ChangePassword(req.CurrentPassword, req.NewPassword, cfg.App.DataDir, params)
		switch {
		case err == nil:
			return c.JSON(fiber.Map{"status": "changed"})
		case errors.Is(err, masterkey.ErrIncorrectPassword):
			return fiber.NewError(fiber.StatusUnauthorized, "current password is incorrect")
		case errors.Is(err, masterkey.ErrPasswordTooShort):
			return fiber.NewError(fiber.StatusBadRequest, err.Error())
		default:
			return fiber.NewError(fiber.StatusInternalServerError, "changing master key failed")
		}
	}
}
