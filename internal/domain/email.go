package domain

import "time"

// Email is one archived message (FR-ST-03). ToAddrs/CcAddrs/Flags round-trip
// through JSON arrays in SQLite; this struct holds them already decoded.
type Email struct {
	ID              int64
	MessageID       string
	AccountID       int64
	FolderID        int64
	UID             uint32
	Subject         string
	FromAddr        string
	ToAddrs         []string
	CcAddrs         []string
	Date            time.Time
	Size            int64
	HasAttachments  bool
	Flags           []string
	StorageLocation string // "local" or "s3"
	LocalPath       string
	S3Key           string
	S3ETag          string
	S3SHA256        string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}
