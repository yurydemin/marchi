package notify

import (
	"context"
	"errors"
	"testing"
)

type failingNotifier struct{ err error }

func (f *failingNotifier) Notify(ctx context.Context, e Event) error { return f.err }

func TestMultiNotifier_FansOutToAll(t *testing.T) {
	a := &recordingNotifier{}
	b := &recordingNotifier{}
	m := &MultiNotifier{Notifiers: []Notifier{a, b}}

	if err := m.Notify(context.Background(), Event{Kind: "sync_failed"}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if a.count() != 1 || b.count() != 1 {
		t.Errorf("a=%d b=%d, want both to receive the event", a.count(), b.count())
	}
}

func TestMultiNotifier_OneFailureDoesNotBlockOthers(t *testing.T) {
	failing := &failingNotifier{err: errors.New("boom")}
	ok := &recordingNotifier{}
	var reportedErrs []error

	m := &MultiNotifier{
		Notifiers: []Notifier{failing, ok},
		OnError:   func(n Notifier, err error) { reportedErrs = append(reportedErrs, err) },
	}

	if err := m.Notify(context.Background(), Event{Kind: "sync_failed"}); err != nil {
		t.Fatalf("Notify returned an error, want nil (best-effort): %v", err)
	}
	if ok.count() != 1 {
		t.Error("the working notifier never ran after the failing one")
	}
	if len(reportedErrs) != 1 {
		t.Errorf("OnError called %d times, want 1", len(reportedErrs))
	}
}
