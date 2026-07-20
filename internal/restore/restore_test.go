package restore

import (
	"context"
	"database/sql"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yurydemin/marchi/internal/account"
	"github.com/yurydemin/marchi/internal/db"
	"github.com/yurydemin/marchi/internal/db/repo"
	"github.com/yurydemin/marchi/internal/db/writer"
	"github.com/yurydemin/marchi/internal/domain"
)

// TestRestoreOne_AppendFails_FallsBackToSMTP covers FR-RS-02's fallback:
// a target account whose IMAP host is unreachable (guaranteed APPEND
// failure) still restores successfully via the SMTP fallback, recorded
// as method=smtp.
func TestRestoreOne_AppendFails_FallsBackToSMTP(t *testing.T) {
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "marchi.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer sqlDB.Close()
	w := writer.New(sqlDB)
	defer w.Close()

	accountsRepo := repo.NewAccountsRepo(sqlDB, w)
	foldersRepo := repo.NewFoldersRepo(sqlDB, w)
	emailsRepo := repo.NewEmailsRepo(sqlDB, w)
	restoreLogsRepo := repo.NewRestoreLogsRepo(sqlDB, w)

	masterKey := make([]byte, 32)
	mgr, err := account.NewManager(accountsRepo, masterKey)
	if err != nil {
		t.Fatalf("account.NewManager: %v", err)
	}

	smtpAddr, smtpSrv := startFakeSMTPServer(t)
	smtpHost, smtpPortStr, err := net.SplitHostPort(smtpAddr)
	if err != nil {
		t.Fatalf("SplitHostPort: %v", err)
	}
	smtpPort, err := strconv.Atoi(smtpPortStr)
	if err != nil {
		t.Fatalf("Atoi: %v", err)
	}

	// The target account's IMAPHost is a bare loopback address (no
	// "imap." prefix), so deriveSMTPHost reuses it verbatim — that's how
	// trySMTP ends up pointed at smtpHost. smtpPortOverrideForTests
	// substitutes the fake server's random port for the real 587.
	t.Cleanup(func() { smtpPortOverrideForTests = 0 })
	smtpPortOverrideForTests = smtpPort

	targetAccount, err := mgr.AddAccount(context.Background(), account.AddAccountParams{
		Email: "fallback-target@example.com", IMAPHost: smtpHost, IMAPPort: 1, // port 1: nothing listens, guarantees APPEND fails
		IMAPTLS: domain.IMAPTLSNone, IMAPUsername: "fallback-target@example.com", IMAPPassword: "irrelevant123",
	})
	if err != nil {
		t.Fatalf("AddAccount: %v", err)
	}

	folder, err := foldersRepo.UpsertFolder(context.Background(), targetAccount.ID, "INBOX", 1)
	if err != nil {
		t.Fatalf("UpsertFolder: %v", err)
	}
	localPath := filepath.Join(t.TempDir(), "seed.eml")
	raw := []byte("From: a@example.com\r\nSubject: fallback test\r\nMessage-ID: <fallback@example.com>\r\n\r\nBody.\r\n")
	if err := os.WriteFile(localPath, raw, 0o644); err != nil {
		t.Fatalf("writing seed .eml: %v", err)
	}

	var emailID int64
	err = w.Do(context.Background(), func(tx *sql.Tx) error {
		var err error
		emailID, err = emailsRepo.Insert(context.Background(), tx, &domain.Email{
			MessageID: "fallback-source@example.com", AccountID: targetAccount.ID, FolderID: folder.ID, UID: 1,
			StorageLocation: "local", LocalPath: localPath, Date: time.Now(),
		})
		return err
	})
	if err != nil {
		t.Fatalf("inserting seed email: %v", err)
	}

	restorer := New(Deps{EmailsRepo: emailsRepo, AccountsRepo: accountsRepo, RestoreLogsRepo: restoreLogsRepo, Manager: mgr})
	log, err := restorer.RestoreOne(context.Background(), emailID, targetAccount.ID, "INBOX")
	if err != nil {
		t.Fatalf("RestoreOne: %v", err)
	}
	if log.Status != domain.RestoreStatusCompleted || log.Method != domain.RestoreMethodSMTP {
		t.Fatalf("log = %+v, want completed/smtp (APPEND should have failed and triggered the fallback)", log)
	}

	_, _, data := smtpSrv.received()
	if !strings.Contains(string(data), "Message-ID: <fallback@example.com>") {
		t.Errorf("SMTP fallback did not transmit the original content, got:\n%s", data)
	}
}

// TestRestoreOne_BothMethodsFail_RecordsFailed covers the case where
// neither APPEND nor the SMTP fallback succeed — recorded as failed, not
// silently dropped.
func TestRestoreOne_BothMethodsFail_RecordsFailed(t *testing.T) {
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "marchi.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer sqlDB.Close()
	w := writer.New(sqlDB)
	defer w.Close()

	accountsRepo := repo.NewAccountsRepo(sqlDB, w)
	foldersRepo := repo.NewFoldersRepo(sqlDB, w)
	emailsRepo := repo.NewEmailsRepo(sqlDB, w)
	restoreLogsRepo := repo.NewRestoreLogsRepo(sqlDB, w)

	masterKey := make([]byte, 32)
	mgr, err := account.NewManager(accountsRepo, masterKey)
	if err != nil {
		t.Fatalf("account.NewManager: %v", err)
	}

	t.Cleanup(func() { smtpPortOverrideForTests = 0 })
	smtpPortOverrideForTests = 1 // nothing listens on port 1 either

	targetAccount, err := mgr.AddAccount(context.Background(), account.AddAccountParams{
		Email: "both-fail-target@example.com", IMAPHost: "127.0.0.1", IMAPPort: 1,
		IMAPTLS: domain.IMAPTLSNone, IMAPUsername: "both-fail-target@example.com", IMAPPassword: "irrelevant123",
	})
	if err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	folder, err := foldersRepo.UpsertFolder(context.Background(), targetAccount.ID, "INBOX", 1)
	if err != nil {
		t.Fatalf("UpsertFolder: %v", err)
	}
	localPath := filepath.Join(t.TempDir(), "seed.eml")
	if err := os.WriteFile(localPath, []byte("From: a@example.com\r\nSubject: doomed\r\n\r\nBody.\r\n"), 0o644); err != nil {
		t.Fatalf("writing seed .eml: %v", err)
	}

	var emailID int64
	err = w.Do(context.Background(), func(tx *sql.Tx) error {
		var err error
		emailID, err = emailsRepo.Insert(context.Background(), tx, &domain.Email{
			MessageID: "both-fail-source@example.com", AccountID: targetAccount.ID, FolderID: folder.ID, UID: 1,
			StorageLocation: "local", LocalPath: localPath, Date: time.Now(),
		})
		return err
	})
	if err != nil {
		t.Fatalf("inserting seed email: %v", err)
	}

	restorer := New(Deps{EmailsRepo: emailsRepo, AccountsRepo: accountsRepo, RestoreLogsRepo: restoreLogsRepo, Manager: mgr})
	log, err := restorer.RestoreOne(context.Background(), emailID, targetAccount.ID, "INBOX")
	if err != nil {
		t.Fatalf("RestoreOne: %v", err)
	}
	if log.Status != domain.RestoreStatusFailed {
		t.Fatalf("log = %+v, want failed (neither APPEND nor SMTP could possibly succeed)", log)
	}
	if log.ErrorMsg == "" {
		t.Error("ErrorMsg is empty, want a combined error explaining both failures")
	}
}
