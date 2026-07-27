-- Failure notifications (Phase 5 P0-2): a singleton row, like s3_config
-- and retention_settings — one instance, one set of "how do I want to
-- hear about problems" settings, not per-account. Webhook and email are
-- independent channels; either, both, or neither can be enabled.
CREATE TABLE notification_settings (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    webhook_enabled INTEGER NOT NULL DEFAULT 0,
    webhook_url TEXT NOT NULL DEFAULT '',
    webhook_secret_encrypted BLOB,
    email_enabled INTEGER NOT NULL DEFAULT 0,
    smtp_host TEXT NOT NULL DEFAULT '',
    smtp_port INTEGER NOT NULL DEFAULT 587,
    smtp_username TEXT NOT NULL DEFAULT '',
    smtp_password_encrypted BLOB,
    smtp_from TEXT NOT NULL DEFAULT '',
    smtp_to TEXT NOT NULL DEFAULT '',
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
