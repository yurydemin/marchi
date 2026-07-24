-- Backs the archive-time dedup check (a restored email re-fetched by a
-- later sync must not be archived a second time under a new UID — see
-- internal/sync/fetch.go's duplicate-message_id check): a lookup of
-- "does this account already have an email with this Message-ID"
-- runs once per fetched message, so it needs an index of its own rather
-- than relying on idx_emails_message_id (message_id alone) plus a
-- row-by-row account_id filter.
CREATE INDEX idx_emails_account_message_id ON emails(account_id, message_id);
