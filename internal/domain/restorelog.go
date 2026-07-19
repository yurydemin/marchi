package domain

import "time"

// RestoreLog status/method values (FR-RS-04).
const (
	RestoreStatusCompleted = "completed"
	RestoreStatusFailed    = "failed"

	RestoreMethodIMAPAppend = "imap_append"
	RestoreMethodSMTP       = "smtp"
)

// RestoreLog is one row of restore_logs — a record of a single email
// having been restored (or attempted) into a target account/folder
// (FR-RS-01/04).
type RestoreLog struct {
	ID              int64
	EmailID         int64
	TargetAccountID int64
	TargetFolder    string
	Method          string // "imap_append" or "smtp"
	Status          string // "completed" or "failed"
	ErrorMsg        string
	CreatedAt       time.Time
}
