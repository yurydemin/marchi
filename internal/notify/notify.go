// Package notify sends failure notifications through independent
// channels (webhook, email) so a background sync/retention failure, an
// S3 upload backlog, or low disk space doesn't sit silently until
// someone happens to open the Web UI. Every Notifier here is best-effort
// by design: a failure to deliver a notification must never itself fail
// the operation that triggered it, and one channel failing must never
// stop another from being tried.
package notify

import (
	"context"
	"time"
)

// Event is one thing worth telling the operator about. Kind is a stable,
// short identifier (e.g. "sync_failed", "retention_failed",
// "s3_queue_backlog", "disk_space_low") — callers and Throttle both key
// on it, so it should stay constant across occurrences of the same kind
// of problem. AccountEmail is empty for events that aren't about one
// specific account (disk space, S3 queue backlog).
type Event struct {
	Kind         string
	Message      string
	AccountEmail string
	Time         time.Time
	Meta         map[string]any
}

// Notifier delivers one Event through some channel. Implementations
// should treat delivery failure as a normal error return, not a panic —
// callers (MultiNotifier, the scheduler) decide how to handle it.
type Notifier interface {
	Notify(ctx context.Context, e Event) error
}

// MultiNotifier fans an Event out to every configured Notifier. It
// always returns nil: a notification channel being down (SMTP relay
// unreachable, webhook endpoint timing out) is exactly the kind of
// transient failure that must not cascade into failing whatever
// triggered the notification in the first place. Per-notifier failures
// go to OnError instead, if set — for logging, not propagation.
type MultiNotifier struct {
	Notifiers []Notifier
	OnError   func(n Notifier, err error)
}

func (m *MultiNotifier) Notify(ctx context.Context, e Event) error {
	for _, n := range m.Notifiers {
		if err := n.Notify(ctx, e); err != nil && m.OnError != nil {
			m.OnError(n, err)
		}
	}
	return nil
}
