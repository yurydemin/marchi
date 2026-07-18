-- s3_config is a singleton settings row (FR-S3-02), not a full table of
-- per-account rows like accounts: S3 is one connection shared by the
-- whole instance. id is pinned to 1 by the CHECK constraint so there can
-- only ever be exactly one row; the repo layer always upserts id=1
-- rather than issuing a normal INSERT.
--
-- access_key_encrypted/secret_key_encrypted are AES-256-GCM ciphertext
-- under the Master Key (NFR-SC-02), the same convention as
-- accounts.imap_password_encrypted — deliberately not in config.yaml,
-- see that file's S3Config doc comment for why.
CREATE TABLE s3_config (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    enabled INTEGER NOT NULL DEFAULT 0,
    endpoint TEXT NOT NULL DEFAULT '',
    region TEXT NOT NULL DEFAULT '',
    bucket TEXT NOT NULL DEFAULT '',
    access_key_encrypted BLOB,
    secret_key_encrypted BLOB,
    path_style INTEGER NOT NULL DEFAULT 0,
    storage_class TEXT NOT NULL DEFAULT 'STANDARD',
    tls_skip_verify INTEGER NOT NULL DEFAULT 0,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
