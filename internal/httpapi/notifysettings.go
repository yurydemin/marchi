package httpapi

import (
	"database/sql"
	"errors"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/yurydemin/marchi/internal/domain"
	"github.com/yurydemin/marchi/internal/notify"
	"github.com/yurydemin/marchi/internal/notifyconfig"
)

// registerNotificationSettings wires the Notification Settings API
// (Phase 5 P0-2): CRUD over the notification_settings singleton and a
// per-channel saved-settings test-send, mirroring registerS3Settings'
// shape.
func registerNotificationSettings(app *fiber.App, vault *vaultState) {
	app.Get("/api/v1/notifications/settings", handleGetNotificationSettings(vault))
	app.Put("/api/v1/notifications/settings", handleSaveNotificationSettings(vault))
	app.Post("/api/v1/notifications/settings/test", handleTestNotificationSettings(vault))
}

// notificationSettingsResponse never includes the encrypted secret
// bytes, the same convention s3SettingsResponse follows — the *Configured
// booleans tell the UI whether a secret/password is set without exposing
// anything derived from it.
type notificationSettingsResponse struct {
	WebhookEnabled          bool   `json:"webhook_enabled"`
	WebhookURL              string `json:"webhook_url"`
	WebhookSecretConfigured bool   `json:"webhook_secret_configured"`

	EmailEnabled           bool   `json:"email_enabled"`
	SMTPHost               string `json:"smtp_host"`
	SMTPPort               int    `json:"smtp_port"`
	SMTPUsername           string `json:"smtp_username"`
	SMTPPasswordConfigured bool   `json:"smtp_password_configured"`
	SMTPFrom               string `json:"smtp_from"`
	SMTPTo                 string `json:"smtp_to"`

	UpdatedAt time.Time `json:"updated_at"`
}

func notificationSettingsResponseFrom(s *domain.NotificationSettings) notificationSettingsResponse {
	return notificationSettingsResponse{
		WebhookEnabled: s.WebhookEnabled, WebhookURL: s.WebhookURL,
		WebhookSecretConfigured: len(s.WebhookSecretEncrypted) > 0,
		EmailEnabled:            s.EmailEnabled, SMTPHost: s.SMTPHost, SMTPPort: s.SMTPPort,
		SMTPUsername:           s.SMTPUsername,
		SMTPPasswordConfigured: len(s.SMTPPasswordEncrypted) > 0,
		SMTPFrom:               s.SMTPFrom, SMTPTo: s.SMTPTo,
		UpdatedAt: s.UpdatedAt,
	}
}

func handleGetNotificationSettings(vault *vaultState) fiber.Handler {
	return func(c *fiber.Ctx) error {
		b, err := currentBackendOrLocked(vault)
		if err != nil {
			return err
		}
		s, err := b.notifyConfigMgr.Get(c.Context())
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return c.Status(fiber.StatusOK).JSON(fiber.Map{"configured": false})
			}
			return fiber.NewError(fiber.StatusInternalServerError, "loading notification settings failed")
		}
		return c.JSON(fiber.Map{"configured": true, "settings": notificationSettingsResponseFrom(s)})
	}
}

type saveNotificationSettingsRequest struct {
	WebhookEnabled bool   `json:"webhook_enabled"`
	WebhookURL     string `json:"webhook_url"`
	WebhookSecret  string `json:"webhook_secret"` // "" keeps the currently stored secret

	EmailEnabled bool   `json:"email_enabled"`
	SMTPHost     string `json:"smtp_host"`
	SMTPPort     int    `json:"smtp_port"`
	SMTPUsername string `json:"smtp_username"`
	SMTPPassword string `json:"smtp_password"` // "" keeps the currently stored password
	SMTPFrom     string `json:"smtp_from"`
	SMTPTo       string `json:"smtp_to"`
}

// handleSaveNotificationSettings persists new settings but, like S3's own
// Settings API, does not hot-swap the running Scheduler/Uploader's
// Notifier — that was built once at unlock time (backend.notifier). A
// saved change here takes effect on the next unlock/restart, the same
// trade-off startS3Components already makes and documents for S3
// settings changed via its own Settings API.
func handleSaveNotificationSettings(vault *vaultState) fiber.Handler {
	return func(c *fiber.Ctx) error {
		b, err := currentBackendOrLocked(vault)
		if err != nil {
			return err
		}
		var req saveNotificationSettingsRequest
		if err := c.BodyParser(&req); err != nil {
			return fiber.NewError(fiber.StatusBadRequest, "invalid request body")
		}

		s, err := b.notifyConfigMgr.Save(c.Context(), notifyconfig.SaveParams{
			WebhookEnabled: req.WebhookEnabled, WebhookURL: req.WebhookURL, WebhookSecret: req.WebhookSecret,
			EmailEnabled: req.EmailEnabled, SMTPHost: req.SMTPHost, SMTPPort: req.SMTPPort,
			SMTPUsername: req.SMTPUsername, SMTPPassword: req.SMTPPassword,
			SMTPFrom: req.SMTPFrom, SMTPTo: req.SMTPTo,
		})
		if err != nil {
			return fiber.NewError(fiber.StatusBadRequest, err.Error())
		}
		return c.JSON(notificationSettingsResponseFrom(s))
	}
}

// handleTestNotificationSettings sends a real test notification through
// every currently-enabled, currently-saved channel (not an unsaved
// payload — the same PUT-then-test flow handleTestS3Settings uses) and
// reports each channel's own success/failure. Unlike notify.MultiNotifier
// (which always returns nil — a production notification failing must
// never fail whatever triggered it), a test-send exists specifically to
// surface per-channel errors to the person configuring it, so this calls
// each notify.Notifier directly instead of going through MultiNotifier.
func handleTestNotificationSettings(vault *vaultState) fiber.Handler {
	return func(c *fiber.Ctx) error {
		b, err := currentBackendOrLocked(vault)
		if err != nil {
			return err
		}
		s, err := b.notifyConfigMgr.Get(c.Context())
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fiber.NewError(fiber.StatusBadRequest, "notifications are not configured yet")
			}
			return fiber.NewError(fiber.StatusInternalServerError, "loading notification settings failed")
		}
		if !s.WebhookEnabled && !s.EmailEnabled {
			return fiber.NewError(fiber.StatusBadRequest, "no channel is enabled")
		}

		webhookSecret, smtpPassword, err := b.notifyConfigMgr.DecryptSecrets(s)
		if err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, "decrypting notification secrets failed")
		}

		testEvent := notify.Event{
			Kind: "test", Message: "This is a test notification from Marchi.", Time: time.Now(),
		}
		results := fiber.Map{}

		if s.WebhookEnabled {
			wn := &notify.WebhookNotifier{URL: s.WebhookURL, Secret: webhookSecret}
			results["webhook"] = testResult(wn.Notify(c.Context(), testEvent))
		}
		if s.EmailEnabled {
			en := &notify.EmailNotifier{
				Host: s.SMTPHost, Port: s.SMTPPort, Username: s.SMTPUsername, Password: smtpPassword,
				From: s.SMTPFrom, To: s.SMTPTo,
			}
			results["email"] = testResult(en.Notify(c.Context(), testEvent))
		}
		return c.JSON(results)
	}
}

func testResult(err error) fiber.Map {
	if err != nil {
		return fiber.Map{"ok": false, "error": err.Error()}
	}
	return fiber.Map{"ok": true}
}
