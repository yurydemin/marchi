package domain

import "time"

// OAuth2 provider identifiers, shared across accounts.oauth2_provider and
// oauth2_apps.provider.
const (
	OAuth2ProviderGoogle    = "google"
	OAuth2ProviderMicrosoft = "microsoft"
)

// OAuth2App is one row of oauth2_apps — the BYO OAuth2 application
// credentials for one provider (поправка #4). ClientSecretEncrypted is
// AES-256-GCM ciphertext under the Master Key's credential subkey.
type OAuth2App struct {
	Provider              string
	ClientID              string
	ClientSecretEncrypted []byte
	RedirectURL           string
	UpdatedAt             time.Time
}
