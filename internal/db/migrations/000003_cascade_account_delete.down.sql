CREATE TABLE sync_logs_old (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    account_id INTEGER NOT NULL REFERENCES accounts(id),
    started_at DATETIME NOT NULL,
    ended_at DATETIME,
    emails_processed INTEGER NOT NULL DEFAULT 0,
    emails_archived INTEGER NOT NULL DEFAULT 0,
    emails_skipped INTEGER NOT NULL DEFAULT 0,
    bytes_downloaded INTEGER NOT NULL DEFAULT 0,
    errors INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'running',
    error_msg TEXT
);
INSERT INTO sync_logs_old SELECT * FROM sync_logs;
DROP TABLE sync_logs;
ALTER TABLE sync_logs_old RENAME TO sync_logs;
CREATE INDEX idx_sync_logs_account ON sync_logs(account_id);
CREATE INDEX idx_sync_logs_status ON sync_logs(status);

CREATE TABLE restore_logs_old (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    email_id INTEGER NOT NULL REFERENCES emails(id),
    target_account_id INTEGER NOT NULL REFERENCES accounts(id),
    target_folder TEXT NOT NULL,
    method TEXT NOT NULL DEFAULT 'imap_append',
    status TEXT NOT NULL DEFAULT 'pending',
    error_msg TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
INSERT INTO restore_logs_old SELECT * FROM restore_logs;
DROP TABLE restore_logs;
ALTER TABLE restore_logs_old RENAME TO restore_logs;
CREATE INDEX idx_restore_logs_email ON restore_logs(email_id);
