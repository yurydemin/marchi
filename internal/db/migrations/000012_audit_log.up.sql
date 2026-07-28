-- Records security/data-relevant actions (vault unlock, restore
-- triggered, email deleted, rule created/updated/deleted) for after-the-
-- fact review — "who did what, when" independent of the much more
-- granular restore_logs/sync_logs tables, which record operation
-- *outcomes*, not the fact that a human (or a script acting as one)
-- requested the action in the first place.
CREATE TABLE audit_log (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    event_type TEXT NOT NULL,
    ip TEXT, -- empty for CLI-triggered events, which have no request IP
    summary TEXT NOT NULL
);

CREATE INDEX idx_audit_log_created_at ON audit_log(created_at DESC);
