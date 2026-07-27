package scheduler

import (
	"context"
	"sync"
	"testing"

	"github.com/yurydemin/marchi/internal/account"
	"github.com/yurydemin/marchi/internal/db/repo"
	"github.com/yurydemin/marchi/internal/domain"
	"github.com/yurydemin/marchi/internal/notify"
	"github.com/yurydemin/marchi/internal/retention"
)

type recordingNotifier struct {
	mu     sync.Mutex
	events []notify.Event
}

func (r *recordingNotifier) Notify(ctx context.Context, e notify.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
	return nil
}

func (r *recordingNotifier) all() []notify.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]notify.Event, len(r.events))
	copy(out, r.events)
	return out
}

func TestScheduler_SyncOne_NotifiesOnFailure(t *testing.T) {
	env := newTestEnv(t)
	a, err := env.deps.Manager.AddAccount(context.Background(), account.AddAccountParams{
		Email: "a@example.com", IMAPHost: "127.0.0.1", IMAPPort: 1, // nothing listens here
		IMAPTLS: domain.IMAPTLSNone, IMAPPassword: "hunter2hunter2",
	})
	if err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	n := &recordingNotifier{}
	env.deps.Notifier = n
	s := newTestScheduler(t, env)

	s.syncOne(a.ID, "test-job-id")

	events := n.all()
	if len(events) != 1 {
		t.Fatalf("got %d notifications, want 1", len(events))
	}
	if events[0].Kind != "sync_failed" {
		t.Errorf("Kind = %q, want %q", events[0].Kind, "sync_failed")
	}
	if events[0].AccountEmail != "a@example.com" {
		t.Errorf("AccountEmail = %q, want %q", events[0].AccountEmail, "a@example.com")
	}
}

func TestScheduler_SyncOne_NoNotifierConfigured_DoesNotPanic(t *testing.T) {
	env := newTestEnv(t)
	a, err := env.deps.Manager.AddAccount(context.Background(), account.AddAccountParams{
		Email: "a@example.com", IMAPHost: "127.0.0.1", IMAPPort: 1,
		IMAPTLS: domain.IMAPTLSNone, IMAPPassword: "hunter2hunter2",
	})
	if err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	s := newTestScheduler(t, env) // env.deps.Notifier left nil

	s.syncOne(a.ID, "test-job-id") // must not panic
}

func TestScheduler_RunRetention_NotifiesOnFailure(t *testing.T) {
	env := newTestEnv(t)
	env.deps.RetentionRunner = retention.New(retention.Deps{
		AccountsRepo: env.deps.AccountsRepo, EmailsRepo: env.deps.EmailsRepo,
		RetentionSettingsRepo: repo.NewRetentionSettingsRepo(env.sqlDB, env.deps.Writer),
		S3ConfigRepo:          repo.NewS3ConfigRepo(env.sqlDB, env.deps.Writer),
		Writer:                env.deps.Writer,
	})
	n := &recordingNotifier{}
	env.deps.Notifier = n
	s := newTestScheduler(t, env)

	// Close the database out from under the runner so AccountsRepo.List
	// fails immediately — the simplest reliable way to force Run's error
	// path without a deeper retention-package mock.
	env.sqlDB.Close()

	s.runRetention(context.Background())

	events := n.all()
	if len(events) != 1 {
		t.Fatalf("got %d notifications, want 1", len(events))
	}
	if events[0].Kind != "retention_failed" {
		t.Errorf("Kind = %q, want %q", events[0].Kind, "retention_failed")
	}
}

func TestScheduler_CheckDiskSpace_FiresWhenBelowThreshold(t *testing.T) {
	orig := freeDiskBytesFunc
	t.Cleanup(func() { freeDiskBytesFunc = orig })
	freeDiskBytesFunc = func(path string) (uint64, error) { return diskSpaceLowThresholdBytes - 1, nil }

	env := newTestEnv(t)
	n := &recordingNotifier{}
	env.deps.Notifier = n
	s := newTestScheduler(t, env)

	s.checkDiskSpace(context.Background())

	events := n.all()
	if len(events) != 1 || events[0].Kind != "disk_space_low" {
		t.Fatalf("events = %+v, want exactly one disk_space_low event", events)
	}
}

func TestScheduler_CheckDiskSpace_DoesNotFireWhenAboveThreshold(t *testing.T) {
	orig := freeDiskBytesFunc
	t.Cleanup(func() { freeDiskBytesFunc = orig })
	freeDiskBytesFunc = func(path string) (uint64, error) { return diskSpaceLowThresholdBytes + 1, nil }

	env := newTestEnv(t)
	n := &recordingNotifier{}
	env.deps.Notifier = n
	s := newTestScheduler(t, env)

	s.checkDiskSpace(context.Background())

	if events := n.all(); len(events) != 0 {
		t.Fatalf("events = %+v, want none when free space is above the threshold", events)
	}
}

func TestScheduler_CheckDiskSpace_NoNotifierConfigured_SkipsTheCheck(t *testing.T) {
	orig := freeDiskBytesFunc
	t.Cleanup(func() { freeDiskBytesFunc = orig })
	called := false
	freeDiskBytesFunc = func(path string) (uint64, error) { called = true; return 0, nil }

	env := newTestEnv(t) // Notifier left nil
	s := newTestScheduler(t, env)

	s.checkDiskSpace(context.Background())

	if called {
		t.Error("freeDiskBytesFunc was called even though no Notifier is configured — wasted work")
	}
}

func TestFreeDiskBytes_ReturnsAPlausibleValue(t *testing.T) {
	free, err := freeDiskBytes(t.TempDir())
	if err != nil {
		t.Fatalf("freeDiskBytes: %v", err)
	}
	if free == 0 {
		t.Error("freeDiskBytes returned 0 for a real, writable temp directory")
	}
}
