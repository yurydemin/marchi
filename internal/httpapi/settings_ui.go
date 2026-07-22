package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"html/template"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/session"

	"github.com/yurydemin/marchi/internal/config"
	"github.com/yurydemin/marchi/internal/domain"
	"github.com/yurydemin/marchi/internal/oauth2config"
	"github.com/yurydemin/marchi/internal/s3config"
	"github.com/yurydemin/marchi/internal/s3store"
	"github.com/yurydemin/marchi/internal/security/masterkey"
)

// settingsPageData is the "settings" page's top-level template data.
// Unlike Accounts/Rules, none of Settings' forms target a <tbody> — each
// has its own small result div — so none of them run into the HTML5
// table-parsing foster-parenting bug those two pages had to work around;
// every form here can safely re-render its own result fragment directly,
// success or error, without any HX-Retarget trick.
type settingsPageData struct {
	Unlocked bool

	S3Configured bool
	S3           s3SettingsResponse

	GoogleApp    *oauth2AppResponse
	MicrosoftApp *oauth2AppResponse

	RetentionConfigured bool
	Retention           retentionSettingsResponse

	SyncLogs []syncLogEntryResponse
}

func registerSettingsPage(app *fiber.App, cfg *config.Config, vault *vaultState, store *session.Store, pages map[string]*template.Template) {
	app.Get("/settings", handleSettingsPage(vault, store, pages))
	app.Post("/settings/master-key", handleSettingsChangeMasterKey(cfg, vault, store, pages))
	app.Put("/settings/s3", handleSettingsSaveS3(vault, store, pages))
	app.Post("/settings/s3/test", handleSettingsTestS3(vault, store, pages))
	app.Put("/settings/oauth2/:provider", handleSettingsSaveOAuth2(vault, store, pages))
	app.Put("/settings/retention", handleSettingsSaveRetention(vault, store, pages))
}

func handleSettingsPage(vault *vaultState, store *session.Store, pages map[string]*template.Template) fiber.Handler {
	return func(c *fiber.Ctx) error {
		c.Set(fiber.HeaderContentType, fiber.MIMETextHTMLCharsetUTF8)
		b, ok := pageBackend(c, vault, store)
		if !ok {
			return renderLocked(c, pages)
		}
		data, err := loadSettingsPageData(c, b)
		if err != nil {
			return err
		}
		data.Unlocked = true
		return render(c, pages, "settings", "layout", data)
	}
}

// loadSettingsPageData gathers every section's current state directly
// from the repos/managers backend already holds — no round-trip through
// this project's own JSON API, the same way every other page's full-page
// GET works (see accounts_ui.go/rules_ui.go's handleAccountsPage/
// handleRulesPage).
func loadSettingsPageData(c *fiber.Ctx, b *backend) (settingsPageData, error) {
	var data settingsPageData

	s3, err := b.s3ConfigManager.Get(c.Context())
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return data, fiber.NewError(fiber.StatusInternalServerError, "loading s3 settings failed")
		}
	} else {
		data.S3Configured = true
		data.S3 = s3SettingsResponseFrom(s3)
	}

	apps, err := b.oauth2ConfigMgr.List(c.Context())
	if err != nil {
		return data, fiber.NewError(fiber.StatusInternalServerError, "loading oauth2 apps failed")
	}
	for _, a := range apps {
		resp := oauth2AppResponseFrom(a)
		switch a.Provider {
		case domain.OAuth2ProviderGoogle:
			data.GoogleApp = &resp
		case domain.OAuth2ProviderMicrosoft:
			data.MicrosoftApp = &resp
		}
	}

	retention, err := b.retentionSettingsRepo.Get(c.Context())
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return data, fiber.NewError(fiber.StatusInternalServerError, "loading retention settings failed")
		}
	} else {
		data.RetentionConfigured = true
		data.Retention = retentionSettingsResponseFrom(retention)
	}

	logs, err := b.syncLogsRepo.ListRecentPage(c.Context(), 0, 20)
	if err != nil {
		return data, fiber.NewError(fiber.StatusInternalServerError, "loading sync logs failed")
	}
	accounts, err := b.accountsRepo.List(c.Context())
	if err != nil {
		return data, fiber.NewError(fiber.StatusInternalServerError, "listing accounts failed")
	}
	emailByAccountID := make(map[int64]string, len(accounts))
	for _, a := range accounts {
		emailByAccountID[a.ID] = a.Email
	}
	data.SyncLogs = make([]syncLogEntryResponse, len(logs))
	for i, l := range logs {
		data.SyncLogs[i] = syncLogEntryResponse{
			ID: l.ID, AccountID: l.AccountID, Email: emailByAccountID[l.AccountID],
			StartedAt: l.StartedAt, EndedAt: l.EndedAt,
			EmailsProcessed: l.EmailsProcessed, EmailsArchived: l.EmailsArchived,
			BytesDownloaded: l.BytesDownloaded, Errors: l.Errors,
			Status: string(l.Status), ErrorMsg: l.ErrorMsg,
		}
	}

	return data, nil
}

// settingsResult is every settings form's shared result-fragment shape:
// a success message, or an error message — never both.
type settingsResult struct {
	OK      bool
	Message string
}

func settingsOK(msg string) settingsResult    { return settingsResult{OK: true, Message: msg} }
func settingsError(msg string) settingsResult { return settingsResult{OK: false, Message: msg} }

func renderSettingsResult(c *fiber.Ctx, pages map[string]*template.Template, name string, res settingsResult) error {
	c.Set(fiber.HeaderContentType, fiber.MIMETextHTMLCharsetUTF8)
	return render(c, pages, "settings", name, res)
}

func handleSettingsChangeMasterKey(cfg *config.Config, vault *vaultState, store *session.Store, pages map[string]*template.Template) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if _, err := requireUnlockedSession(c, vault, store); err != nil {
			return err
		}
		loc := localizer(c)
		var req changeMasterKeyRequest
		if err := c.BodyParser(&req); err != nil {
			return renderSettingsResult(c, pages, "master-key-result", settingsError(loc.T("common.invalid_form")))
		}
		if req.NewPassword != req.ConfirmPassword {
			return renderSettingsResult(c, pages, "master-key-result", settingsError(loc.T("settings.result.password_mismatch")))
		}

		params := masterkey.Argon2Params{
			Memory:      cfg.Security.Argon2.Memory,
			Iterations:  cfg.Security.Argon2.Iterations,
			Parallelism: cfg.Security.Argon2.Parallelism,
		}
		err := masterkey.ChangePassword(req.CurrentPassword, req.NewPassword, cfg.App.DataDir, params)
		switch {
		case err == nil:
			return renderSettingsResult(c, pages, "master-key-result", settingsOK(loc.T("settings.result.master_key_changed")))
		case errors.Is(err, masterkey.ErrIncorrectPassword):
			return renderSettingsResult(c, pages, "master-key-result", settingsError(loc.T("settings.result.current_password_incorrect")))
		case errors.Is(err, masterkey.ErrPasswordTooShort):
			return renderSettingsResult(c, pages, "master-key-result", settingsError(err.Error()))
		default:
			return renderSettingsResult(c, pages, "master-key-result", settingsError("changing master key failed"))
		}
	}
}

type settingsS3FormRequest struct {
	Enabled       bool   `form:"enabled"`
	Endpoint      string `form:"endpoint"`
	Region        string `form:"region"`
	Bucket        string `form:"bucket"`
	AccessKey     string `form:"access_key"`
	SecretKey     string `form:"secret_key"`
	PathStyle     bool   `form:"path_style"`
	StorageClass  string `form:"storage_class"`
	TLSSkipVerify bool   `form:"tls_skip_verify"`
}

func handleSettingsSaveS3(vault *vaultState, store *session.Store, pages map[string]*template.Template) fiber.Handler {
	return func(c *fiber.Ctx) error {
		b, err := requireUnlockedSession(c, vault, store)
		if err != nil {
			return err
		}
		var req settingsS3FormRequest
		if err := c.BodyParser(&req); err != nil {
			return renderSettingsResult(c, pages, "s3-result", settingsError(localizer(c).T("common.invalid_form")))
		}
		if _, err := b.s3ConfigManager.Save(c.Context(), s3config.SaveParams{
			Enabled: req.Enabled, Endpoint: req.Endpoint, Region: req.Region, Bucket: req.Bucket,
			AccessKey: req.AccessKey, SecretKey: req.SecretKey, PathStyle: req.PathStyle,
			StorageClass: req.StorageClass, TLSSkipVerify: req.TLSSkipVerify,
		}); err != nil {
			return renderSettingsResult(c, pages, "s3-result", settingsError(err.Error()))
		}
		return renderSettingsResult(c, pages, "s3-result", settingsOK(localizer(c).T("settings.result.s3_saved")))
	}
}

func handleSettingsTestS3(vault *vaultState, store *session.Store, pages map[string]*template.Template) fiber.Handler {
	return func(c *fiber.Ctx) error {
		b, err := requireUnlockedSession(c, vault, store)
		if err != nil {
			return err
		}
		s, err := b.s3ConfigManager.Get(c.Context())
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return renderSettingsResult(c, pages, "s3-test-result", settingsError(localizer(c).T("settings.result.s3_not_configured")))
			}
			return renderSettingsResult(c, pages, "s3-test-result", settingsError("loading s3 settings failed"))
		}
		accessKey, secretKey, err := b.s3ConfigManager.DecryptCredentials(s)
		if err != nil {
			return renderSettingsResult(c, pages, "s3-test-result", settingsError("decrypting s3 credentials failed"))
		}
		client, err := s3store.NewClient(s3store.Options{
			Endpoint: s.Endpoint, Region: s.Region, Bucket: s.Bucket,
			AccessKeyID: accessKey, SecretAccessKey: secretKey,
			PathStyle: s.PathStyle, TLSSkipVerify: s.TLSSkipVerify,
		})
		if err != nil {
			return renderSettingsResult(c, pages, "s3-test-result", settingsError(err.Error()))
		}
		pingCtx, cancel := context.WithTimeout(c.Context(), 10*time.Second)
		defer cancel()
		if err := client.Ping(pingCtx); err != nil {
			return renderSettingsResult(c, pages, "s3-test-result", settingsError(err.Error()))
		}
		return renderSettingsResult(c, pages, "s3-test-result", settingsOK(localizer(c).T("settings.result.s3_connected")))
	}
}

type settingsOAuth2FormRequest struct {
	ClientID     string `form:"client_id"`
	ClientSecret string `form:"client_secret"`
	RedirectURL  string `form:"redirect_url"`
}

func handleSettingsSaveOAuth2(vault *vaultState, store *session.Store, pages map[string]*template.Template) fiber.Handler {
	return func(c *fiber.Ctx) error {
		b, err := requireUnlockedSession(c, vault, store)
		if err != nil {
			return err
		}
		provider := c.Params("provider")
		resultName := "oauth2-" + provider + "-result"
		var req settingsOAuth2FormRequest
		if err := c.BodyParser(&req); err != nil {
			return renderSettingsResult(c, pages, resultName, settingsError(localizer(c).T("common.invalid_form")))
		}
		if _, err := b.oauth2ConfigMgr.Save(c.Context(), oauth2config.SaveParams{
			Provider: provider, ClientID: req.ClientID, ClientSecret: req.ClientSecret, RedirectURL: req.RedirectURL,
		}); err != nil {
			return renderSettingsResult(c, pages, resultName, settingsError(err.Error()))
		}
		return renderSettingsResult(c, pages, resultName, settingsOK(localizer(c).T("settings.result.oauth2_saved")))
	}
}

type settingsRetentionFormRequest struct {
	DefaultLocalDays    string `form:"default_local_days"`
	DefaultMoveToS3Days string `form:"default_move_to_s3_days"`
	DefaultS3Days       string `form:"default_s3_days"`
}

func handleSettingsSaveRetention(vault *vaultState, store *session.Store, pages map[string]*template.Template) fiber.Handler {
	return func(c *fiber.Ctx) error {
		b, err := requireUnlockedSession(c, vault, store)
		if err != nil {
			return err
		}
		var req settingsRetentionFormRequest
		if err := c.BodyParser(&req); err != nil {
			return renderSettingsResult(c, pages, "retention-result", settingsError(localizer(c).T("common.invalid_form")))
		}
		local, err := parseOptionalDays(req.DefaultLocalDays)
		if err != nil {
			return renderSettingsResult(c, pages, "retention-result", settingsError("local days: "+err.Error()))
		}
		moveToS3, err := parseOptionalDays(req.DefaultMoveToS3Days)
		if err != nil {
			return renderSettingsResult(c, pages, "retention-result", settingsError("move-to-S3 days: "+err.Error()))
		}
		s3Days, err := parseOptionalDays(req.DefaultS3Days)
		if err != nil {
			return renderSettingsResult(c, pages, "retention-result", settingsError("S3 days: "+err.Error()))
		}
		if err := b.retentionSettingsRepo.Upsert(c.Context(), &domain.RetentionSettings{
			DefaultLocalDays: local, DefaultMoveToS3Days: moveToS3, DefaultS3Days: s3Days,
		}); err != nil {
			return renderSettingsResult(c, pages, "retention-result", settingsError("saving retention settings failed"))
		}
		return renderSettingsResult(c, pages, "retention-result", settingsOK(localizer(c).T("settings.result.retention_saved")))
	}
}

// parseOptionalDays parses a retention day-count form field where an
// empty string means "unlimited" (nil), matching
// domain.RetentionSettings' own *int = nil convention.
func parseOptionalDays(s string) (*int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return nil, errors.New("must be a whole number of days, or blank for unlimited")
	}
	if n < 0 {
		return nil, errors.New("must not be negative")
	}
	return &n, nil
}
