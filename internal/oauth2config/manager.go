// Package oauth2config is the OAuth2 BYO App Settings Manager (поправка
// #4, Phase 3 step 13): the business-logic layer between the HTTP API
// and repo.OAuth2AppsRepo, mirroring internal/s3config's role for S3
// connection settings and internal/account's for IMAP credentials.
package oauth2config

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/yurydemin/marchi/internal/account"
	"github.com/yurydemin/marchi/internal/db/repo"
	"github.com/yurydemin/marchi/internal/domain"
	oauth2pkg "github.com/yurydemin/marchi/internal/oauth2"
	"github.com/yurydemin/marchi/internal/security/crypto"
)

// oauth2AppAADPrefix binds an encrypted client secret to its own
// provider — "google" and "microsoft" ciphertexts can't be swapped for
// each other undetected, the same idea accounts.imap_password_encrypted
// binds against email.
const oauth2AppAADPrefix = "oauth2-app:"

// Manager wraps OAuth2AppsRepo with the credential-encryption subkey
// (account.CredentialSubkey — the same one IMAP passwords and S3
// credentials use, per FR-ST-05's "one Master Key, per-purpose subkeys"
// applied at the "credential" use-case level).
type Manager struct {
	repo *repo.OAuth2AppsRepo
	key  []byte
}

func NewManager(oauth2AppsRepo *repo.OAuth2AppsRepo, masterKey []byte) (*Manager, error) {
	key, err := account.CredentialSubkey(masterKey)
	if err != nil {
		return nil, err
	}
	return &Manager{repo: oauth2AppsRepo, key: key}, nil
}

// SaveParams is the plaintext input for registering or updating a
// provider's BYO OAuth2 app.
type SaveParams struct {
	Provider     string
	ClientID     string
	ClientSecret string // "" on update keeps the existing encrypted value
	RedirectURL  string
}

// Save validates params, encrypts ClientSecret (if provided), and
// upserts provider's row.
func (m *Manager) Save(ctx context.Context, p SaveParams) (*domain.OAuth2App, error) {
	if err := validateSaveParams(p); err != nil {
		return nil, err
	}

	existing, err := m.repo.Get(ctx, p.Provider)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("oauth2config: loading existing app: %w", err)
	}

	app := &domain.OAuth2App{Provider: p.Provider, ClientID: p.ClientID, RedirectURL: p.RedirectURL}
	if p.ClientSecret != "" {
		enc, err := crypto.Encrypt(m.key, []byte(p.ClientSecret), []byte(oauth2AADFor(p.Provider)))
		if err != nil {
			return nil, fmt.Errorf("oauth2config: encrypting client secret: %w", err)
		}
		app.ClientSecretEncrypted = enc
	} else if existing != nil {
		app.ClientSecretEncrypted = existing.ClientSecretEncrypted
	}

	if err := m.repo.Upsert(ctx, app); err != nil {
		return nil, err
	}
	return m.repo.Get(ctx, p.Provider)
}

// Get returns provider's saved app config, or sql.ErrNoRows if never configured.
func (m *Manager) Get(ctx context.Context, provider string) (*domain.OAuth2App, error) {
	return m.repo.Get(ctx, provider)
}

// List returns every configured provider's app.
func (m *Manager) List(ctx context.Context) ([]*domain.OAuth2App, error) {
	return m.repo.List(ctx)
}

// DecryptClientSecret returns app's plaintext client secret.
func (m *Manager) DecryptClientSecret(app *domain.OAuth2App) (string, error) {
	secret, err := crypto.Decrypt(m.key, app.ClientSecretEncrypted, []byte(oauth2AADFor(app.Provider)))
	if err != nil {
		return "", fmt.Errorf("oauth2config: decrypting client secret: %w", err)
	}
	return string(secret), nil
}

// BuildApp loads and decrypts provider's app, ready to drive
// internal/oauth2's authorization-code flow.
func (m *Manager) BuildApp(ctx context.Context, provider string) (oauth2pkg.App, error) {
	app, err := m.repo.Get(ctx, provider)
	if err != nil {
		return oauth2pkg.App{}, err
	}
	secret, err := m.DecryptClientSecret(app)
	if err != nil {
		return oauth2pkg.App{}, err
	}
	return oauth2pkg.App{
		Provider: app.Provider, ClientID: app.ClientID, ClientSecret: secret, RedirectURL: app.RedirectURL,
	}, nil
}

// RefreshToken builds provider's App and refreshes current — structurally
// satisfies account.OAuth2TokenRefresher without account needing to
// import this package (which itself imports account for
// CredentialSubkey; the reverse import would cycle).
func (m *Manager) RefreshToken(ctx context.Context, provider string, current oauth2pkg.Token) (oauth2pkg.Token, error) {
	app, err := m.BuildApp(ctx, provider)
	if err != nil {
		return oauth2pkg.Token{}, fmt.Errorf("oauth2config: loading app for refresh: %w", err)
	}
	return app.Refresh(ctx, current)
}

func oauth2AADFor(provider string) string {
	return oauth2AppAADPrefix + provider
}

func validateSaveParams(p SaveParams) error {
	if p.Provider != domain.OAuth2ProviderGoogle && p.Provider != domain.OAuth2ProviderMicrosoft {
		return fmt.Errorf("oauth2config: unknown provider %q", p.Provider)
	}
	if strings.TrimSpace(p.ClientID) == "" {
		return fmt.Errorf("oauth2config: client_id is required")
	}
	if strings.TrimSpace(p.RedirectURL) == "" {
		return fmt.Errorf("oauth2config: redirect_url is required")
	}
	return nil
}
