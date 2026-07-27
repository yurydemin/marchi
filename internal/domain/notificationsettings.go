package domain

import "time"

// NotificationSettings is the singleton failure-notification
// configuration (Phase 5 P0-2), backed by the notification_settings
// table's single row — one instance, one set of "how do I want to hear
// about problems" settings, mirroring S3Settings/RetentionSettings.
// Webhook and email are independent channels; either, both, or neither
// can be enabled.
type NotificationSettings struct {
	WebhookEnabled         bool
	WebhookURL             string
	WebhookSecretEncrypted []byte

	EmailEnabled          bool
	SMTPHost              string
	SMTPPort              int
	SMTPUsername          string
	SMTPPasswordEncrypted []byte
	SMTPFrom              string
	SMTPTo                string

	UpdatedAt time.Time
}
