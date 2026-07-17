DROP INDEX IF EXISTS idx_restore_logs_email;
DROP INDEX IF EXISTS idx_sync_logs_status;
DROP INDEX IF EXISTS idx_sync_logs_account;
DROP INDEX IF EXISTS idx_s3_queue_status;
DROP INDEX IF EXISTS idx_emails_storage;
DROP INDEX IF EXISTS idx_emails_message_id;
DROP INDEX IF EXISTS idx_emails_date;
DROP INDEX IF EXISTS idx_emails_folder;
DROP INDEX IF EXISTS idx_emails_account;

DROP TABLE IF EXISTS restore_logs;
DROP TABLE IF EXISTS sync_logs;
DROP TABLE IF EXISTS s3_upload_queue;
DROP TABLE IF EXISTS rules;
DROP TABLE IF EXISTS attachments;
DROP TABLE IF EXISTS emails;
DROP TABLE IF EXISTS folders;
DROP TABLE IF EXISTS accounts;
