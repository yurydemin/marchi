-- FR-AM-06: deleting an account must cascade to everything that
-- references it. sync_logs.account_id and restore_logs.target_account_id
-- were missed in 000001's initial schema — every other child table
-- (folders, emails, attachments, s3_upload_queue) already has
-- ON DELETE CASCADE, but these two didn't, so deleting an account with
-- any sync history (or, once Phase 3 exists, any restore history)
-- failed outright with a FOREIGN KEY constraint error. SQLite has no
-- ALTER TABLE for changing a column's foreign key, so both tables are
-- rebuilt: new table with the fix, copy the data over, drop the old one,
-- rename into place.

CREATE TABLE sync_logs_new (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
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
INSERT INTO sync_logs_new SELECT * FROM sync_logs;
DROP TABLE sync_logs;
ALTER TABLE sync_logs_new RENAME TO sync_logs;
CREATE INDEX idx_sync_logs_account ON sync_logs(account_id);
CREATE INDEX idx_sync_logs_status ON sync_logs(status);

CREATE TABLE restore_logs_new (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    email_id INTEGER NOT NULL REFERENCES emails(id),
    target_account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    target_folder TEXT NOT NULL,
    method TEXT NOT NULL DEFAULT 'imap_append',
    status TEXT NOT NULL DEFAULT 'pending',
    error_msg TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
INSERT INTO restore_logs_new SELECT * FROM restore_logs;
DROP TABLE restore_logs;
ALTER TABLE restore_logs_new RENAME TO restore_logs;
CREATE INDEX idx_restore_logs_email ON restore_logs(email_id);
