-- next_attempt_at drives the upload queue worker pool's exponential
-- backoff (FR-S3-06): a row is only eligible to be claimed once
-- next_attempt_at <= now. Defaulting to CURRENT_TIMESTAMP makes a
-- freshly-enqueued row immediately eligible, no special-casing needed
-- for the first attempt.
ALTER TABLE s3_upload_queue ADD COLUMN next_attempt_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP;
