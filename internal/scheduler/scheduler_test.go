package scheduler

import (
	"context"
	"crypto/rand"
	"database/sql"
	"io"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/yurydemin/marchi/internal/account"
	"github.com/yurydemin/marchi/internal/config"
	"github.com/yurydemin/marchi/internal/db"
	"github.com/yurydemin/marchi/internal/db/repo"
	"github.com/yurydemin/marchi/internal/db/writer"
	"github.com/yurydemin/marchi/internal/domain"
	"github.com/yurydemin/marchi/internal/search"
	"github.com/yurydemin/marchi/internal/security/crypto"
)

type testEnv struct {
	sqlDB *sql.DB
	cfg   *config.Config
	deps  Deps
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	dataDir := t.TempDir()
	sqlDB, err := db.Open(filepath.Join(dataDir, "marchi.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	w := writer.New(sqlDB)
	t.Cleanup(func() { w.Close() })

	accountsRepo := repo.NewAccountsRepo(sqlDB, w)

	masterKey := make([]byte, crypto.KeySize)
	if _, err := io.ReadFull(rand.Reader, masterKey); err != nil {
		t.Fatal(err)
	}
	mgr, err := account.NewManager(accountsRepo, masterKey)
	if err != nil {
		t.Fatalf("account.NewManager: %v", err)
	}

	cfg := config.Defaults(dataDir)
	cfg.Sync.DefaultSchedule = "0 */6 * * *"
	cfg.Sync.MaxConcurrentAccounts = 2

	return &testEnv{
		sqlDB: sqlDB,
		cfg:   cfg,
		deps: Deps{
			AccountsRepo:    accountsRepo,
			FoldersRepo:     repo.NewFoldersRepo(sqlDB, w),
			EmailsRepo:      repo.NewEmailsRepo(sqlDB, w),
			AttachmentsRepo: repo.NewAttachmentsRepo(sqlDB, w),
			SyncLogsRepo:    repo.NewSyncLogsRepo(sqlDB, w),
			Manager:         mgr,
			Writer:          w,
			Host:            "test-host",
		},
	}
}

func (e *testEnv) createAccount(t *testing.T, email string, active bool) int64 {
	t.Helper()
	id, err := e.deps.AccountsRepo.Create(context.Background(), &domain.Account{
		Email: email, IMAPHost: "127.0.0.1", IMAPPort: 143, IsActive: active,
	})
	if err != nil {
		t.Fatalf("creating account: %v", err)
	}
	return id
}

func (e *testEnv) setAccount(t *testing.T, id int64, syncCron string, active bool) {
	t.Helper()
	var cron any
	if syncCron != "" {
		cron = syncCron
	}
	if _, err := e.sqlDB.Exec(`UPDATE accounts SET sync_cron = ?, is_active = ? WHERE id = ?`,
		cron, boolToInt(active), id); err != nil {
		t.Fatalf("updating account: %v", err)
	}
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func newTestScheduler(t *testing.T, env *testEnv) *Scheduler {
	t.Helper()
	s, err := New(env.cfg, zap.NewNop(), env.deps)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func TestScheduler_Refresh_AddsEntryUsingDefaultSchedule(t *testing.T) {
	env := newTestEnv(t)
	id := env.createAccount(t, "a@example.com", true)
	s := newTestScheduler(t, env)

	s.refresh(context.Background())

	entry, ok := s.entries[id]
	if !ok {
		t.Fatal("expected an entry for the active account")
	}
	if entry.spec != env.cfg.Sync.DefaultSchedule {
		t.Errorf("spec = %q, want the default schedule %q", entry.spec, env.cfg.Sync.DefaultSchedule)
	}
}

func TestScheduler_Refresh_UsesPerAccountCronOverride(t *testing.T) {
	env := newTestEnv(t)
	id := env.createAccount(t, "a@example.com", true)
	env.setAccount(t, id, "*/5 * * * *", true)
	s := newTestScheduler(t, env)

	s.refresh(context.Background())

	entry := s.entries[id]
	if entry.spec != "*/5 * * * *" {
		t.Errorf("spec = %q, want the per-account override", entry.spec)
	}
}

func TestScheduler_Refresh_ReschedulesOnCronChange(t *testing.T) {
	env := newTestEnv(t)
	id := env.createAccount(t, "a@example.com", true)
	s := newTestScheduler(t, env)

	s.refresh(context.Background())
	firstEntryID := s.entries[id].entryID

	env.setAccount(t, id, "*/10 * * * *", true)
	s.refresh(context.Background())

	entry, ok := s.entries[id]
	if !ok {
		t.Fatal("entry disappeared after a cron change")
	}
	if entry.spec != "*/10 * * * *" {
		t.Errorf("spec = %q, want the updated cron expression", entry.spec)
	}
	if entry.entryID == firstEntryID {
		t.Error("expected a new cron entry ID after rescheduling, got the same one")
	}
	if len(s.cronSched.Entries()) != 1 {
		t.Errorf("expected exactly 1 entry registered with the underlying cron scheduler, got %d", len(s.cronSched.Entries()))
	}
}

func TestScheduler_Refresh_RemovesEntryOnDeactivate(t *testing.T) {
	env := newTestEnv(t)
	id := env.createAccount(t, "a@example.com", true)
	s := newTestScheduler(t, env)

	s.refresh(context.Background())
	if _, ok := s.entries[id]; !ok {
		t.Fatal("expected an entry before deactivation")
	}

	env.setAccount(t, id, "", false)
	s.refresh(context.Background())

	if _, ok := s.entries[id]; ok {
		t.Error("expected the entry to be removed after deactivation")
	}
	if len(s.cronSched.Entries()) != 0 {
		t.Errorf("expected 0 entries registered with the underlying cron scheduler, got %d", len(s.cronSched.Entries()))
	}
}

func TestScheduler_Refresh_InvalidCronSkipsAccountWithoutCrashing(t *testing.T) {
	env := newTestEnv(t)
	id := env.createAccount(t, "a@example.com", true)
	env.setAccount(t, id, "not a cron expression", true)
	s := newTestScheduler(t, env)

	s.refresh(context.Background())

	if _, ok := s.entries[id]; ok {
		t.Error("expected no entry for an account with an invalid cron expression")
	}
}

func TestScheduler_StartStop_NoAccounts_DoesNotHang(t *testing.T) {
	env := newTestEnv(t)
	s := newTestScheduler(t, env)

	ctx, cancel := context.WithCancel(context.Background())
	s.Start(ctx)
	cancel()

	done := make(chan struct{})
	go func() {
		s.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop() did not return within 5s")
	}
}

// TestScheduler_SyncOne_DecryptsAndAttemptsSync exercises syncOne's real
// wiring (decrypt password via account.Manager, then call
// internal/sync.SyncAccount) end to end, without cron/pool timing and
// without a real IMAP server: an unreachable host makes SyncAccount fail
// fast, which is enough to prove the plumbing is correct — the IMAP
// protocol itself is already covered by internal/sync's own tests.
func TestScheduler_SyncOne_DecryptsAndAttemptsSync(t *testing.T) {
	env := newTestEnv(t)
	a, err := env.deps.Manager.AddAccount(context.Background(), account.AddAccountParams{
		Email:        "a@example.com",
		IMAPHost:     "127.0.0.1",
		IMAPPort:     1, // nothing listens here; SyncAccount should fail fast, not hang
		IMAPTLS:      domain.IMAPTLSNone,
		IMAPPassword: "hunter2hunter2",
	})
	if err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	s := newTestScheduler(t, env)

	done := make(chan struct{})
	go func() {
		s.syncOne(a.ID, "test-job-id")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("syncOne did not return within 10s against an unreachable host")
	}

	logs, err := env.deps.SyncLogsRepo.ListByAccount(context.Background(), a.ID, 1)
	if err != nil {
		t.Fatalf("ListByAccount: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected syncOne to have recorded a sync_logs row, got %d", len(logs))
	}
	if logs[0].Status != domain.SyncLogFailed {
		t.Errorf("Status = %q, want %q (connection to an unreachable host should fail)", logs[0].Status, domain.SyncLogFailed)
	}
}

// TestScheduler_SyncOne_ResolvesIndexFuncFreshEachCall guards the whole
// point of IndexFunc being a function rather than a plain *search.Index:
// a live reindex (FR-SR-04's admin endpoint, internal/httpapi) swaps the
// index out from under a running server, and the Scheduler must pick up
// that swap on its very next sync — not keep using whatever index existed
// when Deps was first built.
func TestScheduler_SyncOne_ResolvesIndexFuncFreshEachCall(t *testing.T) {
	env := newTestEnv(t)
	a, err := env.deps.Manager.AddAccount(context.Background(), account.AddAccountParams{
		Email: "a@example.com", IMAPHost: "127.0.0.1", IMAPPort: 1,
		IMAPTLS: domain.IMAPTLSNone, IMAPPassword: "hunter2hunter2",
	})
	if err != nil {
		t.Fatalf("AddAccount: %v", err)
	}

	calls := 0
	env.deps.IndexFunc = func() *search.Index {
		calls++
		return nil // the value doesn't matter here, only that it's re-resolved
	}
	s := newTestScheduler(t, env)

	s.syncOne(a.ID, "test-job-id")
	s.syncOne(a.ID, "test-job-id")

	if calls != 2 {
		t.Errorf("IndexFunc called %d time(s) across two syncOne calls, want 2 (resolved fresh each time)", calls)
	}
}

func TestScheduler_SyncOne_NilIndexFunc_DoesNotPanic(t *testing.T) {
	env := newTestEnv(t)
	a, err := env.deps.Manager.AddAccount(context.Background(), account.AddAccountParams{
		Email: "a@example.com", IMAPHost: "127.0.0.1", IMAPPort: 1,
		IMAPTLS: domain.IMAPTLSNone, IMAPPassword: "hunter2hunter2",
	})
	if err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	env.deps.IndexFunc = nil // the zero value — never explicitly set
	s := newTestScheduler(t, env)

	s.syncOne(a.ID, "test-job-id") // must not panic
}

// TestScheduler_TriggerSync_RunsThroughTheSameWorkerPool confirms a
// manually-triggered sync (internal/httpapi's async sync endpoint) goes
// through the exact same bounded, drainable pool a scheduled tick does —
// not a bare detached goroutine main.go's shutdown watchdog wouldn't know
// about — rather than testing progress reporting itself, which needs a
// real IMAP server to ever fire (see internal/sync's own tests, which
// have that harness, for that).
func TestScheduler_TriggerSync_RunsThroughTheSameWorkerPool(t *testing.T) {
	env := newTestEnv(t)
	a, err := env.deps.Manager.AddAccount(context.Background(), account.AddAccountParams{
		Email: "a@example.com", IMAPHost: "127.0.0.1", IMAPPort: 1,
		IMAPTLS: domain.IMAPTLSNone, IMAPPassword: "hunter2hunter2",
	})
	if err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	s := newTestScheduler(t, env)

	jobID, err := s.TriggerSync(a.ID)
	if err != nil {
		t.Fatalf("TriggerSync: %v", err)
	}
	if jobID == "" {
		t.Fatal("TriggerSync returned an empty job id")
	}

	// The pool runs the job asynchronously; wait for it to finish by
	// polling for a sync_logs row rather than assuming a fixed delay.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		logs, err := env.deps.SyncLogsRepo.ListByAccount(context.Background(), a.ID, 1)
		if err == nil && len(logs) == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	logs, err := env.deps.SyncLogsRepo.ListByAccount(context.Background(), a.ID, 1)
	if err != nil || len(logs) != 1 {
		t.Fatalf("expected TriggerSync to have run and recorded a sync_logs row, got %v, %v", logs, err)
	}
}
