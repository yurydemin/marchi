CREATE TABLE accounts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    email TEXT NOT NULL UNIQUE,
    display_name TEXT,
    imap_host TEXT NOT NULL,
    imap_port INTEGER NOT NULL DEFAULT 993,
    imap_tls INTEGER NOT NULL DEFAULT 1, -- 0=none, 1=ssl, 2=starttls
    imap_username TEXT,
    imap_password_encrypted BLOB, -- AES-GCM
    oauth2_provider TEXT, -- google, microsoft, null
    oauth2_token_encrypted BLOB,
    is_active INTEGER NOT NULL DEFAULT 1,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE folders (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    folder_name TEXT NOT NULL, -- IMAP UTF-7 decoded
    uidvalidity INTEGER NOT NULL DEFAULT 0,
    last_uid INTEGER NOT NULL DEFAULT 0,
    sync_enabled INTEGER NOT NULL DEFAULT 1,
    UNIQUE(account_id, folder_name)
);

CREATE TABLE emails (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    message_id TEXT NOT NULL,
    account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    folder_id INTEGER NOT NULL REFERENCES folders(id) ON DELETE CASCADE,
    uid INTEGER NOT NULL,
    subject TEXT,
    from_addr TEXT,
    to_addrs TEXT, -- JSON array
    cc_addrs TEXT, -- JSON array
    date DATETIME,
    size INTEGER NOT NULL DEFAULT 0,
    has_attachments INTEGER NOT NULL DEFAULT 0,
    flags TEXT, -- JSON array of IMAP flags
    storage_location TEXT NOT NULL DEFAULT 'local', -- local, s3
    local_path TEXT,
    s3_key TEXT,
    s3_etag TEXT,
    s3_sha256 TEXT, -- SHA-256 of the original content, before encryption
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(account_id, folder_id, uid)
);

CREATE TABLE attachments (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    email_id INTEGER NOT NULL REFERENCES emails(id) ON DELETE CASCADE,
    filename TEXT NOT NULL,
    mime_type TEXT,
    size INTEGER,
    content_id TEXT,
    local_path TEXT,
    s3_key TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- rules.retention_* is 3 columns, not the 2 in the original tech-spec draft:
-- retention_local_days      how many days an email stays in the local Maildir
-- retention_move_to_s3_days the trigger threshold that actually fires local eviction to S3
-- retention_s3_days         days after which the email is deleted from S3 + SQLite (cascade)
-- All nullable: null = keep forever / never evict / never delete.
CREATE TABLE rules (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    priority INTEGER NOT NULL DEFAULT 0,
    conditions_json TEXT NOT NULL, -- serialized rule tree
    action TEXT NOT NULL DEFAULT 'archive', -- archive, skip, archive_and_delete, archive_and_mark_read
    retention_local_days INTEGER,
    retention_move_to_s3_days INTEGER,
    retention_s3_days INTEGER,
    is_active INTEGER NOT NULL DEFAULT 1,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE s3_upload_queue (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    email_id INTEGER REFERENCES emails(id) ON DELETE CASCADE,
    attachment_id INTEGER REFERENCES attachments(id) ON DELETE CASCADE,
    s3_key TEXT NOT NULL,
    local_path TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending', -- pending, uploading, done, failed, pending_delete
    retry_count INTEGER NOT NULL DEFAULT 0,
    error_msg TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    CHECK (email_id IS NOT NULL OR attachment_id IS NOT NULL)
);

CREATE TABLE sync_logs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    account_id INTEGER NOT NULL REFERENCES accounts(id),
    started_at DATETIME NOT NULL,
    ended_at DATETIME,
    emails_processed INTEGER NOT NULL DEFAULT 0,
    emails_archived INTEGER NOT NULL DEFAULT 0,
    emails_skipped INTEGER NOT NULL DEFAULT 0,
    bytes_downloaded INTEGER NOT NULL DEFAULT 0,
    errors INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'running', -- running, completed, failed, cancelled
    error_msg TEXT
);

CREATE TABLE restore_logs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    email_id INTEGER NOT NULL REFERENCES emails(id),
    target_account_id INTEGER NOT NULL REFERENCES accounts(id),
    target_folder TEXT NOT NULL,
    method TEXT NOT NULL DEFAULT 'imap_append', -- imap_append, smtp
    status TEXT NOT NULL DEFAULT 'pending',
    error_msg TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_emails_account ON emails(account_id);
CREATE INDEX idx_emails_folder ON emails(folder_id);
CREATE INDEX idx_emails_date ON emails(date);
CREATE INDEX idx_emails_message_id ON emails(message_id);
CREATE INDEX idx_emails_storage ON emails(storage_location);
CREATE INDEX idx_s3_queue_status ON s3_upload_queue(status);
CREATE INDEX idx_sync_logs_account ON sync_logs(account_id);
CREATE INDEX idx_sync_logs_status ON sync_logs(status);
CREATE INDEX idx_restore_logs_email ON restore_logs(email_id);
