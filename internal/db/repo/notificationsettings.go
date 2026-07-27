package repo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/yurydemin/marchi/internal/db/writer"
	"github.com/yurydemin/marchi/internal/domain"
)

// NotificationSettingsRepo is the notification_settings table's
// repository — a singleton row, like S3ConfigRepo/RetentionSettingsRepo.
type NotificationSettingsRepo struct {
	db *sql.DB
	w  writer.Writer
}

func NewNotificationSettingsRepo(db *sql.DB, w writer.Writer) *NotificationSettingsRepo {
	return &NotificationSettingsRepo{db: db, w: w}
}

// Get returns the singleton row, or sql.ErrNoRows if notifications have
// never been configured (Upsert has never been called) — the zero-config
// default state, same convention as S3ConfigRepo.Get.
func (r *NotificationSettingsRepo) Get(ctx context.Context) (*domain.NotificationSettings, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT webhook_enabled, webhook_url, webhook_secret_encrypted,
		       email_enabled, smtp_host, smtp_port, smtp_username, smtp_password_encrypted,
		       smtp_from, smtp_to, updated_at
		FROM notification_settings WHERE id = 1`)
	return scanNotificationSettings(row)
}

// Upsert writes the singleton row (id=1), creating it on first use and
// overwriting it entirely thereafter — the Settings UI/API always submits
// the full configuration, same convention as S3ConfigRepo.Upsert.
func (r *NotificationSettingsRepo) Upsert(ctx context.Context, s *domain.NotificationSettings) error {
	return r.w.Do(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO notification_settings (
				id, webhook_enabled, webhook_url, webhook_secret_encrypted,
				email_enabled, smtp_host, smtp_port, smtp_username, smtp_password_encrypted,
				smtp_from, smtp_to, updated_at
			) VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
			ON CONFLICT(id) DO UPDATE SET
				webhook_enabled = excluded.webhook_enabled,
				webhook_url = excluded.webhook_url,
				webhook_secret_encrypted = excluded.webhook_secret_encrypted,
				email_enabled = excluded.email_enabled,
				smtp_host = excluded.smtp_host,
				smtp_port = excluded.smtp_port,
				smtp_username = excluded.smtp_username,
				smtp_password_encrypted = excluded.smtp_password_encrypted,
				smtp_from = excluded.smtp_from,
				smtp_to = excluded.smtp_to,
				updated_at = excluded.updated_at`,
			boolToInt(s.WebhookEnabled), s.WebhookURL, s.WebhookSecretEncrypted,
			boolToInt(s.EmailEnabled), s.SMTPHost, s.SMTPPort, s.SMTPUsername, s.SMTPPasswordEncrypted,
			s.SMTPFrom, s.SMTPTo,
		)
		if err != nil {
			return fmt.Errorf("repo: upserting notification_settings: %w", err)
		}
		return nil
	})
}

func scanNotificationSettings(row rowScanner) (*domain.NotificationSettings, error) {
	var (
		s                            domain.NotificationSettings
		webhookEnabled, emailEnabled int
		updatedAt                    string
	)
	err := row.Scan(
		&webhookEnabled, &s.WebhookURL, &s.WebhookSecretEncrypted,
		&emailEnabled, &s.SMTPHost, &s.SMTPPort, &s.SMTPUsername, &s.SMTPPasswordEncrypted,
		&s.SMTPFrom, &s.SMTPTo, &updatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		return nil, fmt.Errorf("repo: scanning notification_settings: %w", err)
	}
	s.WebhookEnabled = webhookEnabled != 0
	s.EmailEnabled = emailEnabled != 0
	s.UpdatedAt, err = parseSQLiteTime(updatedAt)
	if err != nil {
		return nil, fmt.Errorf("repo: parsing updated_at: %w", err)
	}
	return &s, nil
}
