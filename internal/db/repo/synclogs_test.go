package repo

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/yurydemin/marchi/internal/db"
	"github.com/yurydemin/marchi/internal/db/writer"
	"github.com/yurydemin/marchi/internal/domain"
)

func openTestSyncLogsRepo(t *testing.T) (*SyncLogsRepo, *AccountsRepo) {
	t.Helper()
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "mailvault.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	w := writer.New(sqlDB)
	t.Cleanup(func() { w.Close() })

	return NewSyncLogsRepo(sqlDB, w), NewAccountsRepo(sqlDB, w)
}

func TestSyncLogsRepo_StartThenFinish(t *testing.T) {
	logs, accounts := openTestSyncLogsRepo(t)
	ctx := context.Background()
	accountID := mustCreateAccount(t, accounts)

	id, err := logs.Start(ctx, accountID)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if id == 0 {
		t.Fatal("expected a non-zero sync log ID")
	}

	running, err := logs.ListByAccount(ctx, accountID, 10)
	if err != nil {
		t.Fatalf("ListByAccount: %v", err)
	}
	if len(running) != 1 {
		t.Fatalf("got %d logs, want 1", len(running))
	}
	if running[0].Status != domain.SyncLogRunning {
		t.Errorf("Status = %q, want running", running[0].Status)
	}
	if running[0].StartedAt.IsZero() {
		t.Error("expected StartedAt to be populated")
	}
	if !running[0].EndedAt.IsZero() {
		t.Error("expected EndedAt to still be zero before Finish")
	}

	err = logs.Finish(ctx, id, &domain.SyncLog{
		EmailsProcessed: 10, EmailsArchived: 9, EmailsSkipped: 0,
		BytesDownloaded: 45000, Errors: 1,
		Status: domain.SyncLogCompleted, ErrorMsg: "",
	})
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}

	finished, err := logs.ListByAccount(ctx, accountID, 10)
	if err != nil {
		t.Fatalf("ListByAccount: %v", err)
	}
	l := finished[0]
	if l.Status != domain.SyncLogCompleted {
		t.Errorf("Status = %q, want completed", l.Status)
	}
	if l.EmailsProcessed != 10 || l.EmailsArchived != 9 {
		t.Errorf("counts: processed=%d archived=%d", l.EmailsProcessed, l.EmailsArchived)
	}
	if l.BytesDownloaded != 45000 {
		t.Errorf("BytesDownloaded = %d", l.BytesDownloaded)
	}
	if l.Errors != 1 {
		t.Errorf("Errors = %d", l.Errors)
	}
	if l.EndedAt.IsZero() {
		t.Error("expected EndedAt populated after Finish")
	}
}

func TestSyncLogsRepo_FailedRunRecordsErrorMsg(t *testing.T) {
	logs, accounts := openTestSyncLogsRepo(t)
	ctx := context.Background()
	accountID := mustCreateAccount(t, accounts)

	id, err := logs.Start(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	err = logs.Finish(ctx, id, &domain.SyncLog{
		Status: domain.SyncLogFailed, ErrorMsg: "connection refused",
	})
	if err != nil {
		t.Fatal(err)
	}

	list, err := logs.ListByAccount(ctx, accountID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if list[0].Status != domain.SyncLogFailed {
		t.Errorf("Status = %q", list[0].Status)
	}
	if list[0].ErrorMsg != "connection refused" {
		t.Errorf("ErrorMsg = %q", list[0].ErrorMsg)
	}
}

func TestSyncLogsRepo_ListByAccount_NewestFirst(t *testing.T) {
	logs, accounts := openTestSyncLogsRepo(t)
	ctx := context.Background()
	accountID := mustCreateAccount(t, accounts)

	var ids []int64
	for i := 0; i < 3; i++ {
		id, err := logs.Start(ctx, accountID)
		if err != nil {
			t.Fatal(err)
		}
		if err := logs.Finish(ctx, id, &domain.SyncLog{Status: domain.SyncLogCompleted}); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}

	list, err := logs.ListByAccount(ctx, accountID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("got %d logs, want 3", len(list))
	}
	if list[0].ID != ids[2] || list[2].ID != ids[0] {
		t.Errorf("expected newest-first order, got IDs %d, %d, %d", list[0].ID, list[1].ID, list[2].ID)
	}
}

func TestSyncLogsRepo_ListByAccount_RespectsLimit(t *testing.T) {
	logs, accounts := openTestSyncLogsRepo(t)
	ctx := context.Background()
	accountID := mustCreateAccount(t, accounts)

	for i := 0; i < 5; i++ {
		if _, err := logs.Start(ctx, accountID); err != nil {
			t.Fatal(err)
		}
	}

	list, err := logs.ListByAccount(ctx, accountID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Errorf("got %d logs, want 2 (limit)", len(list))
	}
}

func TestSyncLogsRepo_ListRecent_AcrossAccounts(t *testing.T) {
	logs, accounts := openTestSyncLogsRepo(t)
	ctx := context.Background()

	a1 := mustCreateAccount(t, accounts)
	a2, err := accounts.Create(ctx, accountFixture("second@example.com"))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := logs.Start(ctx, a1); err != nil {
		t.Fatal(err)
	}
	if _, err := logs.Start(ctx, a2); err != nil {
		t.Fatal(err)
	}

	list, err := logs.ListRecent(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("got %d logs, want 2", len(list))
	}
}

func TestSyncLogsRepo_NoLogsYet(t *testing.T) {
	logs, accounts := openTestSyncLogsRepo(t)
	ctx := context.Background()
	accountID := mustCreateAccount(t, accounts)

	list, err := logs.ListByAccount(ctx, accountID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Errorf("got %d logs, want 0", len(list))
	}
}
