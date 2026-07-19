package domain

import "time"

// RetentionSettings is the singleton archive-wide retention default
// (FR-RE-04's "глобальные retention defaults"), backed by the
// retention_settings table's single row. An account can override any of
// these three via its own Retention*Days fields (nil there means
// "inherit this"). See internal/retention's package doc for the
// three-stage model these thresholds drive.
type RetentionSettings struct {
	DefaultLocalDays    *int
	DefaultMoveToS3Days *int
	DefaultS3Days       *int
	UpdatedAt           time.Time
}
