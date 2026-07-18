package webui

import (
	"fmt"
	"html/template"
	"time"
)

// funcs are available to every parsed page template.
var funcs = template.FuncMap{
	"humanBytes": humanBytes,
	"formatTime": formatTime,
	"formatDate": formatDate,
	"add":        func(a, b int) int { return a + b },
}

// humanBytes renders n using the same binary (1024-based) units users see
// in most file managers, e.g. 1536 -> "1.5 KiB".
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// formatTime renders *t in a fixed, timezone-explicit format so the
// Dashboard doesn't depend on client-side JS to be readable. Takes a
// pointer since the fields it's fed (e.g. accountStatsResponse.LastSyncAt)
// are themselves optional pointers; callers are expected to have already
// guarded against nil with {{if}}.
func formatTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format("2006-01-02 15:04 MST")
}

// formatDate is formatTime's non-optional counterpart, for fields that
// are always a real time.Time rather than "maybe never happened yet".
func formatDate(t time.Time) string {
	return t.Format("2006-01-02 15:04 MST")
}
