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
	// S3OnlySince is when this email entered Stage B of the retention
	// lifecycle (S3-only, local copy evicted) — zero if it's never left
	// Stage A. internal/retention's Stage B->C threshold counts from this
	// moment, not from CreatedAt.
	S3OnlySince time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}
