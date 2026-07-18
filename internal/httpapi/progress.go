package httpapi

import (
	"fmt"

	"github.com/yurydemin/marchi/internal/domain"
	"github.com/yurydemin/marchi/internal/reindex"
	syncengine "github.com/yurydemin/marchi/internal/sync"
)

// syncWSEvent builds one wsEvent from a syncengine.Progress update
// (FR-SE-07). done is set on the final event for a job, with errMsg
// (empty on success) folded into the human-readable Message.
func syncWSEvent(jobID string, a *domain.Account, p syncengine.Progress, done bool, errMsg string) wsEvent {
	percent := percentOf(p.Processed, p.Total)
	msg := fmt.Sprintf("%s: %d/%d processed, %d archived, %d errors", a.Email, p.Processed, p.Total, p.Archived, p.Errors)
	if done {
		percent = 100
		if errMsg != "" {
			msg = fmt.Sprintf("%s: sync finished with errors: %s", a.Email, errMsg)
		} else {
			msg = fmt.Sprintf("%s: sync completed, %d archived", a.Email, p.Archived)
		}
	}
	return wsEvent{
		Type: "sync", JobID: jobID, ProgressPercent: percent, Message: msg, Done: done,
		AccountEmail: a.Email, FolderName: p.FolderName, CurrentUID: p.CurrentUID,
		Total: p.Total, Processed: p.Processed, Archived: p.Archived, Errors: p.Errors,
	}
}

// reindexWSEvent builds one wsEvent from a reindex.Stats snapshot
// (FR-SR-04). done is set on the final event for a job, with errMsg
// (empty on success) folded into the human-readable Message.
func reindexWSEvent(jobID string, s reindex.Stats, done bool, errMsg string) wsEvent {
	processed := s.Indexed + s.Skipped + s.Errors
	percent := percentOf(processed, s.Total)
	msg := fmt.Sprintf("reindex: %d/%d processed, %d indexed, %d errors", processed, s.Total, s.Indexed, s.Errors)
	if done {
		percent = 100
		if errMsg != "" {
			msg = fmt.Sprintf("reindex finished with errors: %s", errMsg)
		} else {
			msg = fmt.Sprintf("reindex completed: %d indexed, %d skipped, %d errors", s.Indexed, s.Skipped, s.Errors)
		}
	}
	return wsEvent{
		Type: "reindex", JobID: jobID, ProgressPercent: percent, Message: msg, Done: done,
		Total: s.Total, Processed: processed, Indexed: s.Indexed, Errors: s.Errors,
	}
}

func percentOf(done, total int) float64 {
	if total <= 0 {
		return 0
	}
	p := float64(done) / float64(total) * 100
	if p > 100 {
		p = 100
	}
	return p
}
