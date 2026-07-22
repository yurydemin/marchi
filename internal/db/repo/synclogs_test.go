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
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "marchi.db"))
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

func TestSyncLogsRepo_CountAll(t *testing.T) {
	logs, accounts := openTestSyncLogsRepo(t)
	ctx := context.Background()
	accountID := mustCreateAccount(t, accounts)

	if n, err := logs.CountAll(ctx); err != nil || n != 0 {
		t.Fatalf("CountAll before any runs = %d, %v, want 0, nil", n, err)
	}

	for i := 0; i < 3; i++ {
		if _, err := logs.Start(ctx, accountID); err != nil {
			t.Fatalf("Start: %v", err)
		}
	}

	if n, err := logs.CountAll(ctx); err != nil || n != 3 {
		t.Fatalf("CountAll after 3 runs = %d, %v, want 3, nil", n, err)
	}
}

func TestSyncLogsRepo_CountByStatus(t *testing.T) {
	logs, accounts := openTestSyncLogsRepo(t)
	ctx := context.Background()
	accountID := mustCreateAccount(t, accounts)

	if counts, err := logs.CountByStatus(ctx); err != nil || len(counts) != 0 {
		t.Fatalf("CountByStatus before any runs = %v, %v, want empty, nil", counts, err)
	}

	for i := 0; i < 2; i++ {
		id, err := logs.Start(ctx, accountID)
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		if err := logs.Finish(ctx, id, &domain.SyncLog{Status: domain.SyncLogCompleted}); err != nil {
			t.Fatalf("Finish: %v", err)
		}
	}
	id, err := logs.Start(ctx, accountID)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := logs.Finish(ctx, id, &domain.SyncLog{Status: domain.SyncLogFailed, ErrorMsg: "boom"}); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	counts, err := logs.CountByStatus(ctx)
	if err != nil {
		t.Fatalf("CountByStatus: %v", err)
	}
	if counts[string(domain.SyncLogCompleted)] != 2 {
		t.Errorf("counts[completed] = %d, want 2", counts[string(domain.SyncLogCompleted)])
	}
	if counts[string(domain.SyncLogFailed)] != 1 {
		t.Errorf("counts[failed] = %d, want 1", counts[string(domain.SyncLogFailed)])
	}
}

func TestSyncLogsRepo_ListRecentPage_OffsetAndLimit(t *testing.T) {
	logs, accounts := openTestSyncLogsRepo(t)
	ctx := context.Background()
	accountID := mustCreateAccount(t, accounts)

	var ids []int64
	for i := 0; i < 5; i++ {
		id, err := logs.Start(ctx, accountID)
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		ids = append(ids, id)
	}

	page1, err := logs.ListRecentPage(ctx, 0, 2)
	if err != nil {
		t.Fatalf("ListRecentPage: %v", err)
	}
	if len(page1) != 2 || page1[0].ID != ids[4] || page1[1].ID != ids[3] {
		t.Fatalf("page1 = %+v, want the 2 newest runs (ids %d, %d) first", page1, ids[4], ids[3])
	}

	page2, err := logs.ListRecentPage(ctx, 2, 2)
	if err != nil {
		t.Fatalf("ListRecentPage: %v", err)
	}
	if len(page2) != 2 || page2[0].ID != ids[2] || page2[1].ID != ids[1] {
		t.Fatalf("page2 = %+v, want the next 2 runs (ids %d, %d)", page2, ids[2], ids[1])
	}

	page3, err := logs.ListRecentPage(ctx, 4, 2)
	if err != nil {
		t.Fatalf("ListRecentPage: %v", err)
	}
	if len(page3) != 1 || page3[0].ID != ids[0] {
		t.Fatalf("page3 = %+v, want exactly the oldest run (id %d)", page3, ids[0])
	}
}
