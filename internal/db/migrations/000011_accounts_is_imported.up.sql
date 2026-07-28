-- Marks an account created by `marchi import` (mbox/Maildir/.eml), as
-- opposed to a real IMAP mailbox. Deliberately separate from is_active:
-- an imported account has no IMAP credentials to connect with at all, so
-- the Scheduler must never even attempt it (is_active=0 already handles
-- that), and this column exists purely so callers that DO need to tell
-- the two apart (marchi sync's "this account has no IMAP host to talk
-- to" guard, a future Accounts UI "Imported" badge) can, without having
-- to infer it from a sentinel imap_host value.
ALTER TABLE accounts ADD COLUMN is_imported INTEGER NOT NULL DEFAULT 0;
