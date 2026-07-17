package domain

import "time"

// Attachment is one non-body MIME part of an archived email (FR-SE-05).
// Content itself is never stored separately — the original bytes stay
// exactly where they already are, inside the parent email's .eml file;
// LocalPath/S3Key are for a later phase, if attachments are ever extracted
// into their own objects (e.g. for S3 upload).
type Attachment struct {
	ID        int64
	EmailID   int64
	Filename  string
	MIMEType  string
	Size      int64
	ContentID string
	LocalPath string
	S3Key     string
	CreatedAt time.Time
}
