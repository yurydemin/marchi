package repo

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/yurydemin/marchi/internal/db"
	"github.com/yurydemin/marchi/internal/db/writer"
	"github.com/yurydemin/marchi/internal/domain"
)

func openTestRepo(t *testing.T) (*AccountsRepo, writer.Writer) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mailvault.db")
	sqlDB, err := db.Open(path)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })

	w := writer.New(sqlDB)
	t.Cleanup(func() { w.Close() })

	return NewAccountsRepo(sqlDB, w), w
}

func TestAccountsRepo_CreateAndList(t *testing.T) {
	repo, _ := openTestRepo(t)
	ctx := context.Background()

	id, err := repo.Create(ctx, &domain.Account{
		Email:                 "user@example.com",
		DisplayName:           "Work",
		IMAPHost:              "imap.example.com",
		IMAPPort:              993,
		IMAPTLS:               domain.IMAPTLSSSL,
		IMAPUsername:          "user@example.com",
		IMAPPasswordEncrypted: []byte("ciphertext-not-plaintext"),
		IsActive:              true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if id == 0 {
		t.Fatal("expected a non-zero id")
	}

	accounts, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(accounts) != 1 {
		t.Fatalf("got %d accounts, want 1", len(accounts))
	}

	a := accounts[0]
	if a.ID != id {
		t.Errorf("ID = %d, want %d", a.ID, id)
	}
	if a.Email != "user@example.com" {
		t.Errorf("Email = %q", a.Email)
	}
	if a.DisplayName != "Work" {
		t.Errorf("DisplayName = %q", a.DisplayName)
	}
	if a.IMAPTLS != domain.IMAPTLSSSL {
		t.Errorf("IMAPTLS = %v, want ssl", a.IMAPTLS)
	}
	if string(a.IMAPPasswordEncrypted) != "ciphertext-not-plaintext" {
		t.Errorf("IMAPPasswordEncrypted = %q", a.IMAPPasswordEncrypted)
	}
	if !a.IsActive {
		t.Error("expected IsActive true")
	}
	if a.CreatedAt.IsZero() {
		t.Error("expected CreatedAt to be populated")
	}
	if a.OAuth2Provider != "" {
		t.Errorf("OAuth2Provider = %q, want empty (never set)", a.OAuth2Provider)
	}
	if a.OAuth2TokenEncrypted != nil {
		t.Errorf("OAuth2TokenEncrypted = %v, want nil (never set)", a.OAuth2TokenEncrypted)
	}
}

func TestAccountsRepo_DuplicateEmailRejected(t *testing.T) {
	repo, _ := openTestRepo(t)
	ctx := context.Background()

	acct := &domain.Account{Email: "dup@example.com", IMAPHost: "imap.example.com", IMAPPort: 993, IsActive: true}
	if _, err := repo.Create(ctx, acct); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	_, err := repo.Create(ctx, acct)
	if !errors.Is(err, ErrDuplicateEmail) {
		t.Errorf("second Create error = %v, want ErrDuplicateEmail", err)
	}
}

func TestAccountsRepo_GetByEmail(t *testing.T) {
	repo, _ := openTestRepo(t)
	ctx := context.Background()

	if _, err := repo.Create(ctx, &domain.Account{Email: "find-me@example.com", IMAPHost: "h", IMAPPort: 993, IsActive: true}); err != nil {
		t.Fatal(err)
	}

	a, err := repo.GetByEmail(ctx, "find-me@example.com")
	if err != nil {
		t.Fatalf("GetByEmail: %v", err)
	}
	if a.Email != "find-me@example.com" {
		t.Errorf("Email = %q", a.Email)
	}

	_, err = repo.GetByEmail(ctx, "nobody@example.com")
	if err == nil {
		t.Error("expected an error for a missing email")
	}
}

func TestAccountsRepo_Update(t *testing.T) {
	repo, _ := openTestRepo(t)
	ctx := context.Background()

	id, err := repo.Create(ctx, &domain.Account{
		Email: "user@example.com", DisplayName: "Old", IMAPHost: "old.example.com",
		IMAPPort: 993, IMAPTLS: domain.IMAPTLSSSL, IMAPUsername: "user@example.com",
		IMAPPasswordEncrypted: []byte("old-cipher"), IsActive: true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	err = repo.Update(ctx, &domain.Account{
		ID: id, DisplayName: "New", IMAPHost: "new.example.com",
		IMAPPort: 143, IMAPTLS: domain.IMAPTLSStartTLS, IMAPUsername: "renamed",
		IMAPPasswordEncrypted: []byte("new-cipher"), IsActive: false, SyncCron: "*/5 * * * *",
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	a, err := repo.GetByEmail(ctx, "user@example.com")
	if err != nil {
		t.Fatalf("GetByEmail: %v", err)
	}
	if a.DisplayName != "New" || a.IMAPHost != "new.example.com" || a.IMAPPort != 143 {
		t.Errorf("got %+v, want updated fields", a)
	}
	if a.IMAPTLS != domain.IMAPTLSStartTLS {
		t.Errorf("IMAPTLS = %v, want starttls", a.IMAPTLS)
	}
	if string(a.IMAPPasswordEncrypted) != "new-cipher" {
		t.Errorf("IMAPPasswordEncrypted = %q, want new-cipher", a.IMAPPasswordEncrypted)
	}
	if a.IsActive {
		t.Error("expected IsActive false after update")
	}
	if a.SyncCron != "*/5 * * * *" {
		t.Errorf("SyncCron = %q", a.SyncCron)
	}
	// Email is not one of Update's mutable columns — it must survive unchanged.
	if a.Email != "user@example.com" {
		t.Errorf("Email = %q, want unchanged", a.Email)
	}
}

func TestAccountsRepo_Update_UnknownID(t *testing.T) {
	repo, _ := openTestRepo(t)
	err := repo.Update(context.Background(), &domain.Account{ID: 999, IMAPHost: "h", IMAPPort: 993})
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("Update on unknown id: err = %v, want sql.ErrNoRows", err)
	}
}

func TestAccountsRepo_Delete(t *testing.T) {
	repo, _ := openTestRepo(t)
	ctx := context.Background()

	id, err := repo.Create(ctx, &domain.Account{Email: "gone@example.com", IMAPHost: "h", IMAPPort: 993, IsActive: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := repo.Delete(ctx, id); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if _, err := repo.GetByEmail(ctx, "gone@example.com"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("GetByEmail after delete: err = %v, want sql.ErrNoRows", err)
	}
}

func TestAccountsRepo_Delete_UnknownID(t *testing.T) {
	repo, _ := openTestRepo(t)
	err := repo.Delete(context.Background(), 999)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("Delete on unknown id: err = %v, want sql.ErrNoRows", err)
	}
}

// TestAccountsRepo_Delete_CascadesToEverySiblingTable is a regression test:
// 000001's initial schema gave folders/emails/attachments ON DELETE
// CASCADE on their account_id/email_id foreign keys, but sync_logs was
// missed — deleting an account with any sync history failed outright with
// a FOREIGN KEY constraint error (only caught via a live demo hitting the
// real DELETE /api/v1/accounts/{id} endpoint, since no earlier test ever
// exercised deleting an account that actually had sync_logs rows).
// 000003 fixes it; this test seeds one row in every table that references
// accounts (directly or transitively) and confirms Delete cascades
// through all of them.
func TestAccountsRepo_Delete_CascadesToEverySiblingTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mailvault.db")
	sqlDB, err := db.Open(path)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer sqlDB.Close()
	w := writer.New(sqlDB)
	defer w.Close()

	accountsRepo := NewAccountsRepo(sqlDB, w)
	foldersRepo := NewFoldersRepo(sqlDB, w)
	emailsRepo := NewEmailsRepo(sqlDB, w)
	attachmentsRepo := NewAttachmentsRepo(sqlDB, w)
	syncLogsRepo := NewSyncLogsRepo(sqlDB, w)
	ctx := context.Background()

	accountID, err := accountsRepo.Create(ctx, accountFixture("cascade-check@example.com"))
	if err != nil {
		t.Fatalf("creating account: %v", err)
	}
	folder, err := foldersRepo.UpsertFolder(ctx, accountID, "INBOX", 1)
	if err != nil {
		t.Fatalf("UpsertFolder: %v", err)
	}

	var emailID int64
	err = w.Do(ctx, func(tx *sql.Tx) error {
		var err error
		emailID, err = emailsRepo.Insert(ctx, tx, &domain.Email{
			MessageID: "cascade@example.com", AccountID: accountID, FolderID: folder.ID,
			UID: 1, StorageLocation: "local", LocalPath: "/tmp/cascade.eml",
		})
		return err
	})
	if err != nil {
		t.Fatalf("inserting email: %v", err)
	}
	err = w.Do(ctx, func(tx *sql.Tx) error {
		_, err := attachmentsRepo.Insert(ctx, tx, &domain.Attachment{EmailID: emailID, Filename: "a.pdf"})
		return err
	})
	if err != nil {
		t.Fatalf("inserting attachment: %v", err)
	}

	logID, err := syncLogsRepo.Start(ctx, accountID)
	if err != nil {
		t.Fatalf("SyncLogsRepo.Start: %v", err)
	}
	if err := syncLogsRepo.Finish(ctx, logID, &domain.SyncLog{Status: domain.SyncLogCompleted}); err != nil {
		t.Fatalf("SyncLogsRepo.Finish: %v", err)
	}

	if err := accountsRepo.Delete(ctx, accountID); err != nil {
		t.Fatalf("Delete: %v (cascade should reach folders/emails/attachments/sync_logs)", err)
	}

	if folders, err := foldersRepo.ListByAccount(ctx, accountID); err != nil || len(folders) != 0 {
		t.Errorf("folders after delete: %v, %v — want none left", folders, err)
	}
	if emails, err := emailsRepo.ListByAccount(ctx, accountID); err != nil || len(emails) != 0 {
		t.Errorf("emails after delete: %v, %v — want none left", emails, err)
	}
	if atts, err := attachmentsRepo.ListByEmail(ctx, emailID); err != nil || len(atts) != 0 {
		t.Errorf("attachments after delete: %v, %v — want none left", atts, err)
	}
	if logs, err := syncLogsRepo.ListByAccount(ctx, accountID, 10); err != nil || len(logs) != 0 {
		t.Errorf("sync_logs after delete: %v, %v — want none left", logs, err)
	}
}

func TestAccountsRepo_ListEmpty(t *testing.T) {
	repo, _ := openTestRepo(t)
	accounts, err := repo.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(accounts) != 0 {
		t.Errorf("got %d accounts, want 0", len(accounts))
	}
}
