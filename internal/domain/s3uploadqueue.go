package domain

import "time"

// S3UploadQueue status values (FR-S3-06).
const (
	S3QueueStatusPending       = "pending"
	S3QueueStatusUploading     = "uploading"
	S3QueueStatusDone          = "done"
	S3QueueStatusFailed        = "failed"
	S3QueueStatusPendingDelete = "pending_delete"
)

// S3UploadQueueItem is a row of s3_upload_queue — one pending mirror
// upload (or, per FR-S3-09, a pending delete) for an email or attachment.
type S3UploadQueueItem struct {
	ID            int64
	EmailID       int64 // 0 if this row is for an attachment instead
	AttachmentID  int64 // 0 if this row is for an email instead
	S3Key         string
	LocalPath     string
	Status        string
	RetryCount    int
	ErrorMsg      string
	NextAttemptAt time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}
