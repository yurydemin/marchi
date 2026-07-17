package domain

import "time"

// SyncLogStatus mirrors the sync_logs.status column's allowed values.
type SyncLogStatus string

const (
	SyncLogRunning   SyncLogStatus = "running"
	SyncLogCompleted SyncLogStatus = "completed"
	SyncLogFailed    SyncLogStatus = "failed"
	SyncLogCancelled SyncLogStatus = "cancelled"
)

// SyncLog is one sync run's record (FR-SE-06/07). EndedAt is zero while the
// run is still in progress (Status == SyncLogRunning).
type SyncLog struct {
	ID              int64
	AccountID       int64
	StartedAt       time.Time
	EndedAt         time.Time
	EmailsProcessed int
	EmailsArchived  int
	EmailsSkipped   int
	BytesDownloaded int64
	Errors          int
	Status          SyncLogStatus
	ErrorMsg        string
}
