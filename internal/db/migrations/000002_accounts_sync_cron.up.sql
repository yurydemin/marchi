-- FR-SE-06: per-account cron schedule for background sync. NULL means
-- "use the global sync.default_schedule from config.yaml" — this column
-- only needs to hold a value when an account overrides the default.
ALTER TABLE accounts ADD COLUMN sync_cron TEXT;
