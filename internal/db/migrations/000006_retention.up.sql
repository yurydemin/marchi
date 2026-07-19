-- Retention moves from being a per-rule setting to a global default with
-- an optional per-account override — the same shape accounts.sync_cron
-- already uses for sync.default_schedule. Rules keep governing only the
-- archive/skip/mark_read dispatch decision (FR-RE-03); the per-rule
-- retention_* columns added in 000001 are dropped here since re-evaluating
-- rules against an already-archived email at retention-cron time isn't
-- reliable (several condition types, e.g. from_contains/to_contains, need
-- the full From/To header, which emails never stored — only the bare
-- address survives archival).
ALTER TABLE rules DROP COLUMN retention_local_days;
ALTER TABLE rules DROP COLUMN retention_move_to_s3_days;
ALTER TABLE rules DROP COLUMN retention_s3_days;

-- NULL on all three accounts columns below means "inherit the global
-- default from retention_settings".
ALTER TABLE accounts ADD COLUMN retention_local_days INTEGER;
ALTER TABLE accounts ADD COLUMN retention_move_to_s3_days INTEGER;
ALTER TABLE accounts ADD COLUMN retention_s3_days INTEGER;

-- retention_settings is a singleton row (like s3_config) holding the
-- archive-wide retention defaults (FR-RE-04's "глобальные retention
-- defaults"). NULL on any column means that stage never triggers by
-- default (an account can still opt in via its own override columns).
CREATE TABLE retention_settings (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    default_local_days INTEGER,
    default_move_to_s3_days INTEGER,
    default_s3_days INTEGER,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- s3_only_since records when an email entered Stage B (S3-only, local
-- copy evicted) — retention_s3_days counts from this moment, not from
-- the email's original archival (created_at), since Stage B can start
-- long after Stage A did.
ALTER TABLE emails ADD COLUMN s3_only_since DATETIME;
