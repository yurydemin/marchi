package notify

import (
	"context"
	"sync"
	"time"
)

// Throttle wraps n so that repeated Events of the same Kind (and, if set,
// the same AccountEmail) within cooldown are silently dropped rather than
// re-delivered — without this, a scheduler tick or an upload-queue poll
// that keeps failing every few seconds would otherwise fire a webhook (or
// send an email) just as often. The first occurrence of a given kind
// always gets through immediately; only the repeats within the window
// are suppressed.
func Throttle(n Notifier, cooldown time.Duration) Notifier {
	return &throttled{inner: n, cooldown: cooldown, last: make(map[string]time.Time)}
}

type throttled struct {
	inner    Notifier
	cooldown time.Duration

	mu   sync.Mutex
	last map[string]time.Time
}

func (t *throttled) Notify(ctx context.Context, e Event) error {
	key := e.Kind
	if e.AccountEmail != "" {
		key += "|" + e.AccountEmail
	}

	t.mu.Lock()
	now := time.Now()
	if last, ok := t.last[key]; ok && now.Sub(last) < t.cooldown {
		t.mu.Unlock()
		return nil
	}
	t.last[key] = now
	t.mu.Unlock()

	return t.inner.Notify(ctx, e)
}
