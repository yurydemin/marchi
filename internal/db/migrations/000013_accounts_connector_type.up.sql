-- connector_type distinguishes an IMAP account (the default, everything
-- Marchi has done since Phase 1) from a Gmail API-based one: a Gmail
-- account synced through the native REST API (labels, History API delta
-- sync) instead of IMAP. gmail_history_id is that connector's own
-- incremental-sync cursor (Gmail's History API), analogous to what
-- folders.last_uid is for IMAP but not expressible as a UID at all — it
-- has no meaning outside Gmail's API and doesn't belong on the folders
-- table alongside IMAP-specific watermarks.
ALTER TABLE accounts ADD COLUMN connector_type TEXT NOT NULL DEFAULT 'imap';
ALTER TABLE accounts ADD COLUMN gmail_history_id TEXT;
