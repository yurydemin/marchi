-- match_count/last_matched_at are cumulative rule-firing statistics
-- (P2-15 "статистика правил"): how many times this rule has ever
-- matched a message across every sync run of every account (rules are
-- global, not per-account, so this can't be scoped to a single
-- "last run" without a separate per-run event table — a deliberately
-- simpler cumulative counter instead, updated once per sync run rather
-- than once per message; see internal/sync's rule-match aggregation).
ALTER TABLE rules ADD COLUMN match_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE rules ADD COLUMN last_matched_at TIMESTAMP;
