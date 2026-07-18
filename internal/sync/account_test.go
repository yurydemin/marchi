package sync

import (
	"context"
	"testing"

	"github.com/yurydemin/marchi/internal/db/repo"
	"github.com/yurydemin/marchi/internal/domain"
)

// TestSyncAccount_CancelledContext_RecordsCancelledStatus covers NFR-RL-05
// end to end at the SyncAccount level: a shutdown-cancelled context must
// produce a sync_logs row with status "cancelled", distinct from "failed"
// — a deliberate stop isn't the same thing as a real error.
func TestSyncAccount_CancelledContext_RecordsCancelledStatus(t *testing.T) {
	env := newFetchTestEnv(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	account := &domain.Account{
		ID: env.accountID, Email: "user@example.com",
		IMAPHost: "127.0.0.1", IMAPPort: 143, IMAPTLS: domain.IMAPTLSNone,
	}
	syncLogsRepo := repo.NewSyncLogsRepo(env.sqlDB, env.w)

	_, err := SyncAccount(ctx, account, "pass", env.maildirRoot, "test-host",
		env.w, env.foldersR, env.emailsR, env.attachmentsR, syncLogsRepo, nil, nil)
	if err == nil {
		t.Fatal("expected an error for an already-cancelled context, got nil")
	}

	logs, listErr := syncLogsRepo.ListByAccount(context.Background(), env.accountID, 1)
	if listErr != nil {
		t.Fatalf("ListByAccount: %v", listErr)
	}
	if len(logs) != 1 {
		t.Fatalf("got %d sync_logs rows, want 1", len(logs))
	}
	if logs[0].Status != domain.SyncLogCancelled {
		t.Errorf("Status = %q, want %q", logs[0].Status, domain.SyncLogCancelled)
	}
}
