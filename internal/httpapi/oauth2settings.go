package httpapi

import (
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/yurydemin/marchi/internal/domain"
	"github.com/yurydemin/marchi/internal/oauth2config"
)

// registerOAuth2Settings wires the OAuth2 BYO App Settings API (поправка
// #4, Phase 3 step 14): CRUD over oauth2_apps, one row per provider,
// mirroring registerS3Settings' shape for the S3 config singleton.
func registerOAuth2Settings(app *fiber.App, vault *vaultState) {
	app.Get("/api/v1/oauth2/apps", handleListOAuth2Apps(vault))
	app.Put("/api/v1/oauth2/apps/:provider", handleSaveOAuth2App(vault))
}

// oauth2AppResponse never includes the encrypted secret bytes, the same
// convention s3SettingsResponse follows for S3 credentials.
type oauth2AppResponse struct {
	Provider              string    `json:"provider"`
	ClientID              string    `json:"client_id"`
	RedirectURL           string    `json:"redirect_url"`
	CredentialsConfigured bool      `json:"credentials_configured"`
	UpdatedAt             time.Time `json:"updated_at"`
}

func oauth2AppResponseFrom(a *domain.OAuth2App) oauth2AppResponse {
	return oauth2AppResponse{
		Provider: a.Provider, ClientID: a.ClientID, RedirectURL: a.RedirectURL,
		CredentialsConfigured: len(a.ClientSecretEncrypted) > 0, UpdatedAt: a.UpdatedAt,
	}
}

func handleListOAuth2Apps(vault *vaultState) fiber.Handler {
	return func(c *fiber.Ctx) error {
		b, err := currentBackendOrLocked(vault)
		if err != nil {
			return err
		}
		apps, err := b.oauth2ConfigMgr.List(c.Context())
		if err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, "listing oauth2 apps failed")
		}
		resp := make([]oauth2AppResponse, len(apps))
		for i, a := range apps {
			resp[i] = oauth2AppResponseFrom(a)
		}
		return c.JSON(resp)
	}
}

type saveOAuth2AppRequest struct {
	ClientID     string `json:"client_id" form:"client_id"`
	ClientSecret string `json:"client_secret" form:"client_secret"` // "" keeps the currently stored secret
	RedirectURL  string `json:"redirect_url" form:"redirect_url"`
}

func handleSaveOAuth2App(vault *vaultState) fiber.Handler {
	return func(c *fiber.Ctx) error {
		b, err := currentBackendOrLocked(vault)
		if err != nil {
			return err
		}
		provider := c.Params("provider")
		var req saveOAuth2AppRequest
		if err := c.BodyParser(&req); err != nil {
			return fiber.NewError(fiber.StatusBadRequest, "invalid request body")
		}
		a, err := b.oauth2ConfigMgr.Save(c.Context(), oauth2config.SaveParams{
			Provider: provider, ClientID: req.ClientID, ClientSecret: req.ClientSecret, RedirectURL: req.RedirectURL,
		})
		if err != nil {
			return fiber.NewError(fiber.StatusBadRequest, err.Error())
		}
		return c.JSON(oauth2AppResponseFrom(a))
	}
}
