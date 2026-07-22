package sync

import (
	"context"
	"crypto/rand"
	"io"
	"testing"

	"github.com/yurydemin/marchi/internal/db/repo"
	"github.com/yurydemin/marchi/internal/domain"
	"github.com/yurydemin/marchi/internal/s3config"
	"github.com/yurydemin/marchi/internal/security/crypto"
	"github.com/yurydemin/marchi/internal/testutil/dovecot"
)

// TestSyncAccount_FullOrchestration_RealDovecot exercises SyncAccount's
// own wiring end to end against a real IMAP server — Connect, SyncFolders,
// the rulesRepo.ListActive() load, the s3ConfigRepo.Get() check, and the
// per-folder FetchNewMessages loop all in one real run. FetchNewMessages
// itself already has extensive fake-server coverage (fetch_test.go and
// friends); what's missing before this test is coverage of SyncAccount's
// own orchestration around it, which a fake server can't provide since it
// doesn't implement LIST (needed by SyncFolders, which SyncAccount calls
// before ever reaching FetchNewMessages).
func TestSyncAccount_FullOrchestration_RealDovecot(t *testing.T) {
	env := newFetchTestEnv(t)
	ctx := context.Background()

	srv := dovecot.Start(t, "sync-account-test@dovecot.local", "pass12345")
	srv.AppendMessage(t, "sync-account-test@dovecot.local", "pass12345", "INBOX",
		[]byte("From: sender@example.com\r\nSubject: hello world\r\nMessage-ID: <sync-account-full@example.com>\r\n\r\nBody.\r\n"))

	// An active rule that does NOT match this message — exercises
	// SyncAccount's own rulesRepo.ListActive() call (not just rule
	// evaluation itself, which fetch_rules_test.go already covers) with
	// the "no match, defaults to archive" outcome.
	rulesRepo := repo.NewRulesRepo(env.sqlDB, env.w)
	if _, err := rulesRepo.Create(ctx, &domain.Rule{
		Name:       "skip newsletters",
		Priority:   0,
		Conditions: domain.RuleNode{Type: domain.ConditionSubjectContains, Value: "(?i)newsletter"},
		Action:     domain.ActionSkip,
		IsActive:   true,
	}); err != nil {
		t.Fatalf("creating rule fixture: %v", err)
	}

	// A real s3_config row with Enabled=false — exercises SyncAccount's
	// "err == nil but cfg.Enabled is false" branch specifically, which a
	// missing row (sql.ErrNoRows, the more commonly-tested zero-config
	// state) never reaches.
	s3ConfigRepo := repo.NewS3ConfigRepo(env.sqlDB, env.w)
	s3QueueRepo := repo.NewS3UploadQueueRepo(env.sqlDB, env.w)
	masterKey := make([]byte, crypto.KeySize)
	if _, err := io.ReadFull(rand.Reader, masterKey); err != nil {
		t.Fatal(err)
	}
	s3Mgr, err := s3config.NewManager(s3ConfigRepo, masterKey)
	if err != nil {
		t.Fatalf("s3config.NewManager: %v", err)
	}
	if _, err := s3Mgr.Save(ctx, s3config.SaveParams{
		Enabled: false, Endpoint: "http://127.0.0.1:9000", Region: "us-east-1", Bucket: "unused",
		AccessKey: "unused", SecretKey: "unused",
	}); err != nil {
		t.Fatalf("saving disabled s3 config: %v", err)
	}

	account := &domain.Account{
		ID: env.accountID, Email: "sync-account-test@dovecot.local",
		IMAPHost: srv.Host, IMAPPort: srv.Port, IMAPTLS: domain.IMAPTLSNone,
		IMAPUsername: "sync-account-test@dovecot.local",
	}
	syncLogsRepo := repo.NewSyncLogsRepo(env.sqlDB, env.w)

	results, err := SyncAccount(ctx, account, "pass12345", env.maildirRoot, "test-host",
		env.w, env.foldersR, env.emailsR, env.attachmentsR, syncLogsRepo, rulesRepo, nil,
		s3ConfigRepo, s3QueueRepo, nil)
	if err != nil {
		t.Fatalf("SyncAccount: %v", err)
	}

	var inboxResult *FolderResult
	for i := range results {
		if results[i].Folder.FolderName == "INBOX" {
			inboxResult = &results[i]
		}
	}
	if inboxResult == nil {
		t.Fatal("no INBOX result in SyncAccount's return value")
	}
	if inboxResult.Fetched != 1 {
		t.Errorf("INBOX Fetched = %d, want 1 (the rule doesn't match, message should archive normally)", inboxResult.Fetched)
	}

	logs, err := syncLogsRepo.ListByAccount(ctx, env.accountID, 1)
	if err != nil {
		t.Fatalf("ListByAccount: %v", err)
	}
	if len(logs) != 1 || logs[0].Status != domain.SyncLogCompleted {
		t.Fatalf("sync_logs = %+v, want one completed row", logs)
	}
	if logs[0].EmailsArchived != 1 {
		t.Errorf("EmailsArchived = %d, want 1", logs[0].EmailsArchived)
	}
}
