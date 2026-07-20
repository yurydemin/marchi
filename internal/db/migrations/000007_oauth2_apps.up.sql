-- oauth2_apps holds the BYO OAuth2 application credentials (поправка #4:
-- the user registers their own app with Google/Microsoft and pastes in
-- client_id/client_secret — Marchi ships no shared OAuth client). One
-- row per provider, not a singleton like s3_config/retention_settings,
-- since both providers can be configured independently.
--
-- client_secret_encrypted is AES-256-GCM ciphertext under the Master Key
-- (the same credential-encryption subkey as imap_password_encrypted —
-- see account.CredentialSubkey).
CREATE TABLE oauth2_apps (
    provider TEXT PRIMARY KEY CHECK (provider IN ('google', 'microsoft')),
    client_id TEXT NOT NULL,
    client_secret_encrypted BLOB NOT NULL,
    redirect_url TEXT NOT NULL,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
