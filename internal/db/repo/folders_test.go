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

func openTestFoldersRepo(t *testing.T) (*FoldersRepo, *AccountsRepo) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mailvault.db")
	sqlDB, err := db.Open(path)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })

	w := writer.New(sqlDB)
	t.Cleanup(func() { w.Close() })

	return NewFoldersRepo(sqlDB, w), NewAccountsRepo(sqlDB, w)
}

func mustCreateAccount(t *testing.T, accounts *AccountsRepo) int64 {
	t.Helper()
	id, err := accounts.Create(context.Background(), accountFixture("owner@example.com"))
	if err != nil {
		t.Fatalf("creating account fixture: %v", err)
	}
	return id
}

func accountFixture(email string) *domain.Account {
	return &domain.Account{
		Email:                 email,
		IMAPHost:              "imap.example.com",
		IMAPPort:              993,
		IMAPTLS:               domain.IMAPTLSSSL,
		IMAPPasswordEncrypted: []byte("ciphertext"),
		IsActive:              true,
	}
}

func TestUpsertFolder_NewFolder(t *testing.T) {
	folders, accounts := openTestFoldersRepo(t)
	accountID := mustCreateAccount(t, accounts)

	f, err := folders.UpsertFolder(context.Background(), accountID, "INBOX", 100)
	if err != nil {
		t.Fatalf("UpsertFolder: %v", err)
	}
	if f.AccountID != accountID {
		t.Errorf("AccountID = %d, want %d", f.AccountID, accountID)
	}
	if f.FolderName != "INBOX" {
		t.Errorf("FolderName = %q", f.FolderName)
	}
	if f.UIDValidity != 100 {
		t.Errorf("UIDValidity = %d, want 100", f.UIDValidity)
	}
	if f.LastUID != 0 {
		t.Errorf("LastUID = %d, want 0 for a brand new folder", f.LastUID)
	}
	if !f.SyncEnabled {
		t.Error("expected SyncEnabled true by default")
	}
}

func TestUpsertFolder_SameUIDValidityPreservesLastUID(t *testing.T) {
	folders, accounts := openTestFoldersRepo(t)
	accountID := mustCreateAccount(t, accounts)
	ctx := context.Background()

	if _, err := folders.UpsertFolder(ctx, accountID, "INBOX", 100); err != nil {
		t.Fatalf("initial UpsertFolder: %v", err)
	}
	// Simulate the Sync Engine (a later step) having advanced last_uid
	// after fetching some messages.
	if _, err := folders.db.ExecContext(ctx, `UPDATE folders SET last_uid = 42 WHERE folder_name = 'INBOX'`); err != nil {
		t.Fatalf("simulating advanced last_uid: %v", err)
	}

	f, err := folders.UpsertFolder(ctx, accountID, "INBOX", 100)
	if err != nil {
		t.Fatalf("second UpsertFolder: %v", err)
	}
	if f.LastUID != 42 {
		t.Errorf("LastUID = %d, want 42 preserved (UIDVALIDITY unchanged)", f.LastUID)
	}
}

func TestUpsertFolder_ChangedUIDValidityResetsLastUID(t *testing.T) {
	folders, accounts := openTestFoldersRepo(t)
	accountID := mustCreateAccount(t, accounts)
	ctx := context.Background()

	if _, err := folders.UpsertFolder(ctx, accountID, "INBOX", 100); err != nil {
		t.Fatalf("initial UpsertFolder: %v", err)
	}
	if _, err := folders.db.ExecContext(ctx, `UPDATE folders SET last_uid = 42 WHERE folder_name = 'INBOX'`); err != nil {
		t.Fatalf("simulating advanced last_uid: %v", err)
	}

	// Server reports a different UIDVALIDITY — e.g. the mailbox was rebuilt.
	f, err := folders.UpsertFolder(ctx, accountID, "INBOX", 200)
	if err != nil {
		t.Fatalf("UpsertFolder with new UIDVALIDITY: %v", err)
	}
	if f.UIDValidity != 200 {
		t.Errorf("UIDValidity = %d, want 200", f.UIDValidity)
	}
	if f.LastUID != 0 {
		t.Errorf("LastUID = %d, want 0 reset (UIDVALIDITY changed, FR-SE-02 full resync)", f.LastUID)
	}
}

func TestFoldersRepo_GetByID(t *testing.T) {
	folders, accounts := openTestFoldersRepo(t)
	accountID := mustCreateAccount(t, accounts)
	ctx := context.Background()

	created, err := folders.UpsertFolder(ctx, accountID, "INBOX", 100)
	if err != nil {
		t.Fatalf("UpsertFolder: %v", err)
	}

	got, err := folders.GetByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.FolderName != "INBOX" || got.AccountID != accountID {
		t.Errorf("got = %+v, want FolderName=INBOX AccountID=%d", got, accountID)
	}
}

func TestFoldersRepo_GetByID_NotFound(t *testing.T) {
	folders, _ := openTestFoldersRepo(t)

	_, err := folders.GetByID(context.Background(), 999999)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("err = %v, want sql.ErrNoRows", err)
	}
}

func TestFoldersRepo_ListByAccount(t *testing.T) {
	folders, accounts := openTestFoldersRepo(t)
	accountID := mustCreateAccount(t, accounts)
	ctx := context.Background()

	for _, name := range []string{"INBOX", "Archive", "Sent"} {
		if _, err := folders.UpsertFolder(ctx, accountID, name, 1); err != nil {
			t.Fatalf("UpsertFolder(%s): %v", name, err)
		}
	}

	otherAccountID, err := accounts.Create(ctx, accountFixture("someone-else@example.com"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := folders.UpsertFolder(ctx, otherAccountID, "INBOX", 1); err != nil {
		t.Fatal(err)
	}

	list, err := folders.ListByAccount(ctx, accountID)
	if err != nil {
		t.Fatalf("ListByAccount: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("got %d folders, want 3 (must not include the other account's)", len(list))
	}
	if list[0].FolderName != "Archive" {
		t.Errorf("expected alphabetical order, first = %q", list[0].FolderName)
	}
}
