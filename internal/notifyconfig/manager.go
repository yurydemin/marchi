// Package notifyconfig is the Notification Settings Manager (Phase 5
// P0-2): the business-logic layer between the HTTP API and
// repo.NotificationSettingsRepo, mirroring internal/s3config's role for
// S3 credentials. It's the only place that knows how to turn a plaintext
// webhook secret / SMTP password into what actually gets stored —
// repo.NotificationSettingsRepo just persists whatever bytes it's given.
package notifyconfig

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/yurydemin/marchi/internal/account"
	"github.com/yurydemin/marchi/internal/db/repo"
	"github.com/yurydemin/marchi/internal/domain"
	"github.com/yurydemin/marchi/internal/notify"
	"github.com/yurydemin/marchi/internal/security/crypto"
)

// notifyConfigAAD binds the encrypted webhook secret / SMTP password to
// this specific use — notification_settings is a singleton row, so
// there's no per-record id to bind against the way
// accounts.imap_password_encrypted binds to email.
const notifyConfigAAD = "notification-config"

// Manager wraps NotificationSettingsRepo with the credential-encryption
// subkey (account.CredentialSubkey — the same one IMAP passwords and S3
// keys use).
type Manager struct {
	repo *repo.NotificationSettingsRepo
	key  []byte
}

// NewManager derives the credential-encryption subkey from masterKey.
// Call this once per unlock.
func NewManager(notificationSettingsRepo *repo.NotificationSettingsRepo, masterKey []byte) (*Manager, error) {
	key, err := account.CredentialSubkey(masterKey)
	if err != nil {
		return nil, err
	}
	return &Manager{repo: notificationSettingsRepo, key: key}, nil
}

// SaveParams is the plaintext input for creating or updating the
// notification settings singleton. WebhookSecret/SMTPPassword are
// optional on an update: empty keeps whatever is already stored, the
// same convention S3's SaveParams.AccessKey/SecretKey use.
type SaveParams struct {
	WebhookEnabled bool
	WebhookURL     string
	WebhookSecret  string // "" on update keeps the existing encrypted value

	EmailEnabled bool
	SMTPHost     string
	SMTPPort     int
	SMTPUsername string
	SMTPPassword string // "" on update keeps the existing encrypted value
	SMTPFrom     string
	SMTPTo       string
}

// Save validates params, encrypts WebhookSecret/SMTPPassword (if
// provided), and upserts the singleton row. Returns the persisted
// settings.
func (m *Manager) Save(ctx context.Context, p SaveParams) (*domain.NotificationSettings, error) {
	if err := validateSaveParams(p); err != nil {
		return nil, err
	}

	existing, err := m.repo.Get(ctx)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("notifyconfig: loading existing settings: %w", err)
	}

	s := &domain.NotificationSettings{
		WebhookEnabled: p.WebhookEnabled, WebhookURL: p.WebhookURL,
		EmailEnabled: p.EmailEnabled, SMTPHost: p.SMTPHost, SMTPPort: p.SMTPPort,
		SMTPUsername: p.SMTPUsername, SMTPFrom: p.SMTPFrom, SMTPTo: p.SMTPTo,
	}
	if s.SMTPPort == 0 {
		s.SMTPPort = 587
	}

	if p.WebhookSecret != "" {
		enc, err := crypto.Encrypt(m.key, []byte(p.WebhookSecret), []byte(notifyConfigAAD))
		if err != nil {
			return nil, fmt.Errorf("notifyconfig: encrypting webhook secret: %w", err)
		}
		s.WebhookSecretEncrypted = enc
	} else if existing != nil {
		s.WebhookSecretEncrypted = existing.WebhookSecretEncrypted
	}

	if p.SMTPPassword != "" {
		enc, err := crypto.Encrypt(m.key, []byte(p.SMTPPassword), []byte(notifyConfigAAD))
		if err != nil {
			return nil, fmt.Errorf("notifyconfig: encrypting smtp password: %w", err)
		}
		s.SMTPPasswordEncrypted = enc
	} else if existing != nil {
		s.SMTPPasswordEncrypted = existing.SMTPPasswordEncrypted
	}

	if err := m.repo.Upsert(ctx, s); err != nil {
		return nil, err
	}
	return m.repo.Get(ctx)
}

// Get returns the currently saved settings, or repo's sql.ErrNoRows if
// notifications have never been configured.
func (m *Manager) Get(ctx context.Context) (*domain.NotificationSettings, error) {
	return m.repo.Get(ctx)
}

// DecryptSecrets returns s's plaintext webhook secret and SMTP password —
// used right before building a notify.Notifier (BuildNotifier below, or
// a settings test-send), never stored or logged beyond that. Either
// return value is "" if the corresponding _Encrypted field was never set
// (e.g. a webhook configured without a signing secret).
func (m *Manager) DecryptSecrets(s *domain.NotificationSettings) (webhookSecret, smtpPassword string, err error) {
	if len(s.WebhookSecretEncrypted) > 0 {
		plain, err := crypto.Decrypt(m.key, s.WebhookSecretEncrypted, []byte(notifyConfigAAD))
		if err != nil {
			return "", "", fmt.Errorf("notifyconfig: decrypting webhook secret: %w", err)
		}
		webhookSecret = string(plain)
	}
	if len(s.SMTPPasswordEncrypted) > 0 {
		plain, err := crypto.Decrypt(m.key, s.SMTPPasswordEncrypted, []byte(notifyConfigAAD))
		if err != nil {
			return "", "", fmt.Errorf("notifyconfig: decrypting smtp password: %w", err)
		}
		smtpPassword = string(plain)
	}
	return webhookSecret, smtpPassword, nil
}

// BuildNotifier constructs a notify.Notifier from s (decrypting its
// secrets via DecryptSecrets), enabling only the channels s actually has
// turned on. Returns nil if neither channel is enabled — the caller's
// usual nil-means-off convention.
func (m *Manager) BuildNotifier(s *domain.NotificationSettings) (notify.Notifier, error) {
	webhookSecret, smtpPassword, err := m.DecryptSecrets(s)
	if err != nil {
		return nil, err
	}

	var notifiers []notify.Notifier
	if s.WebhookEnabled && s.WebhookURL != "" {
		notifiers = append(notifiers, &notify.WebhookNotifier{URL: s.WebhookURL, Secret: webhookSecret})
	}
	if s.EmailEnabled && s.SMTPHost != "" {
		notifiers = append(notifiers, &notify.EmailNotifier{
			Host: s.SMTPHost, Port: s.SMTPPort, Username: s.SMTPUsername, Password: smtpPassword,
			From: s.SMTPFrom, To: s.SMTPTo,
		})
	}
	if len(notifiers) == 0 {
		return nil, nil
	}
	return &notify.MultiNotifier{Notifiers: notifiers}, nil
}

func validateSaveParams(p SaveParams) error {
	if p.WebhookEnabled && p.WebhookURL == "" {
		return fmt.Errorf("notifyconfig: webhook_url is required when the webhook channel is enabled")
	}
	if p.EmailEnabled {
		if p.SMTPHost == "" {
			return fmt.Errorf("notifyconfig: smtp_host is required when the email channel is enabled")
		}
		if p.SMTPFrom == "" {
			return fmt.Errorf("notifyconfig: smtp_from is required when the email channel is enabled")
		}
		if p.SMTPTo == "" {
			return fmt.Errorf("notifyconfig: smtp_to is required when the email channel is enabled")
		}
	}
	return nil
}
