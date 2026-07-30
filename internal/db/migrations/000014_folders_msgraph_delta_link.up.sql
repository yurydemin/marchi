-- msgraph_delta_link is a ConnectorMSGraph account's per-folder
-- incremental-sync cursor (Microsoft Graph's delta query — see
-- internal/msgraph and internal/sync.SyncAccountMSGraph). Unlike
-- ConnectorGmailAPI's single mailbox-wide gmail_history_id (accounts
-- table), Graph's delta query is scoped per mail folder, so this cursor
-- has to live here rather than on accounts. NULL means no successful
-- sync of this folder has completed yet — the next sync must list every
-- message in it rather than just the delta since a link.
ALTER TABLE folders ADD COLUMN msgraph_delta_link TEXT;
