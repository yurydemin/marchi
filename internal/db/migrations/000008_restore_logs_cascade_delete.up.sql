-- restore_logs.email_id never had ON DELETE CASCADE (only
-- target_account_id got that fix, in 000003 — email_id was missed the
-- same way sync_logs.account_id was). With PRAGMA foreign_keys=ON
-- (поправка #11), deleting an archived email that has restore history
-- fails outright with a FOREIGN KEY constraint error instead of taking
-- its restore_logs rows with it. SQLite has no ALTER TABLE for changing
-- a column's foreign key, so the table is rebuilt: new table with the
-- fix, copy the data over, drop the old one, rename into place — same
-- technique 000003 already used for this exact table.

CREATE TABLE restore_logs_new (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    email_id INTEGER NOT NULL REFERENCES emails(id) ON DELETE CASCADE,
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
