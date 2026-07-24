CREATE TABLE restore_logs_old (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    email_id INTEGER NOT NULL REFERENCES emails(id),
    target_account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
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
