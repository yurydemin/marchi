package notify

import (
	"context"
	"sync"
	"testing"
	"time"
)

type recordingNotifier struct {
	mu     sync.Mutex
	events []Event
}

func (r *recordingNotifier) Notify(ctx context.Context, e Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
	return nil
}

func (r *recordingNotifier) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.events)
}

func TestThrottle_SuppressesRepeatsWithinCooldown(t *testing.T) {
	inner := &recordingNotifier{}
	n := Throttle(inner, time.Hour)

	for i := 0; i < 3; i++ {
		if err := n.Notify(context.Background(), Event{Kind: "disk_space_low"}); err != nil {
			t.Fatalf("Notify: %v", err)
		}
	}
	if got := inner.count(); got != 1 {
		t.Errorf("inner received %d events, want 1 (repeats within cooldown suppressed)", got)
	}
}

func TestThrottle_AllowsAfterCooldownElapses(t *testing.T) {
	inner := &recordingNotifier{}
	n := Throttle(inner, 10*time.Millisecond)

	if err := n.Notify(context.Background(), Event{Kind: "disk_space_low"}); err != nil {
		t.Fatalf("first Notify: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	if err := n.Notify(context.Background(), Event{Kind: "disk_space_low"}); err != nil {
		t.Fatalf("second Notify: %v", err)
	}
	if got := inner.count(); got != 2 {
		t.Errorf("inner received %d events, want 2 (second one after cooldown elapsed)", got)
	}
}

func TestThrottle_DifferentAccountsAreIndependent(t *testing.T) {
	inner := &recordingNotifier{}
	n := Throttle(inner, time.Hour)

	if err := n.Notify(context.Background(), Event{Kind: "sync_failed", AccountEmail: "a@example.com"}); err != nil {
		t.Fatalf("Notify a: %v", err)
	}
	if err := n.Notify(context.Background(), Event{Kind: "sync_failed", AccountEmail: "b@example.com"}); err != nil {
		t.Fatalf("Notify b: %v", err)
	}
	if got := inner.count(); got != 2 {
		t.Errorf("inner received %d events, want 2 (different accounts must not share a cooldown)", got)
	}
}

func TestThrottle_DifferentKindsAreIndependent(t *testing.T) {
	inner := &recordingNotifier{}
	n := Throttle(inner, time.Hour)

	if err := n.Notify(context.Background(), Event{Kind: "sync_failed"}); err != nil {
		t.Fatalf("Notify sync_failed: %v", err)
	}
	if err := n.Notify(context.Background(), Event{Kind: "retention_failed"}); err != nil {
		t.Fatalf("Notify retention_failed: %v", err)
	}
	if got := inner.count(); got != 2 {
		t.Errorf("inner received %d events, want 2 (different kinds must not share a cooldown)", got)
	}
}
