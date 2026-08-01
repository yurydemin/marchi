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
	path := filepath.Join(t.TempDir(), "marchi.db")
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

func TestFoldersRepo_MSGraphDeltaLink_DefaultsEmptyThenRoundTrips(t *testing.T) {
	folders, accounts := openTestFoldersRepo(t)
	accountID := mustCreateAccount(t, accounts)
	ctx := context.Background()

	f, err := folders.UpsertFolder(ctx, accountID, "Inbox", 1)
	if err != nil {
		t.Fatalf("UpsertFolder: %v", err)
	}
	if f.MSGraphDeltaLink != "" {
		t.Errorf("MSGraphDeltaLink = %q, want empty", f.MSGraphDeltaLink)
	}

	link := "https://graph.microsoft.com/v1.0/me/mailFolders/x/messages/delta?$deltatoken=abc123"
	if err := folders.UpdateMSGraphDeltaLink(ctx, f.ID, link); err != nil {
		t.Fatalf("UpdateMSGraphDeltaLink: %v", err)
	}

	got, err := folders.GetByID(ctx, f.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.MSGraphDeltaLink != link {
		t.Errorf("MSGraphDeltaLink = %q, want %q", got.MSGraphDeltaLink, link)
	}

	// UpsertFolder on the same folder (same uidvalidity) must not clobber
	// the delta link — it's a different sync engine's cursor entirely,
	// unrelated to the IMAP-style uidvalidity/last_uid reconciliation
	// UpsertFolder performs.
	f2, err := folders.UpsertFolder(ctx, accountID, "Inbox", 1)
	if err != nil {
		t.Fatalf("UpsertFolder (again): %v", err)
	}
	if f2.MSGraphDeltaLink != link {
		t.Errorf("MSGraphDeltaLink after re-upsert = %q, want %q (unchanged)", f2.MSGraphDeltaLink, link)
	}

	if err := folders.UpdateMSGraphDeltaLink(ctx, f.ID, ""); err != nil {
		t.Fatalf("UpdateMSGraphDeltaLink (clear): %v", err)
	}
	cleared, err := folders.GetByID(ctx, f.ID)
	if err != nil {
		t.Fatalf("GetByID after clearing: %v", err)
	}
	if cleared.MSGraphDeltaLink != "" {
		t.Errorf("MSGraphDeltaLink after clearing = %q, want empty", cleared.MSGraphDeltaLink)
	}
}

func TestFoldersRepo_UpdateMSGraphDeltaLink_UnknownID(t *testing.T) {
	folders, _ := openTestFoldersRepo(t)

	err := folders.UpdateMSGraphDeltaLink(context.Background(), 999999, "link")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("err = %v, want sql.ErrNoRows", err)
	}
}

func TestFoldersRepo_GetOrCreateManual_CreatesNewFolder(t *testing.T) {
	folders, accounts := openTestFoldersRepo(t)
	accountID := mustCreateAccount(t, accounts)
	ctx := context.Background()

	var f *domain.Folder
	err := folders.w.Do(ctx, func(tx *sql.Tx) error {
		var err error
		f, err = folders.GetOrCreateManual(ctx, tx, accountID, "Reorganized")
		return err
	})
	if err != nil {
		t.Fatalf("GetOrCreateManual: %v", err)
	}
	if f.FolderName != "Reorganized" || f.AccountID != accountID {
		t.Errorf("f = %+v, want FolderName=Reorganized AccountID=%d", f, accountID)
	}
	if f.UIDValidity != 0 || f.LastUID != 0 {
		t.Errorf("UIDValidity=%d LastUID=%d, want both 0 for a manually-created folder", f.UIDValidity, f.LastUID)
	}
	if f.SyncEnabled {
		t.Error("expected SyncEnabled false — a manual folder isn't tied to a live source")
	}
}

func TestFoldersRepo_GetOrCreateManual_ReturnsExistingFolderUntouched(t *testing.T) {
	folders, accounts := openTestFoldersRepo(t)
	accountID := mustCreateAccount(t, accounts)
	ctx := context.Background()

	// A real, actively-synced folder with real bookkeeping already advanced.
	real, err := folders.UpsertFolder(ctx, accountID, "Archive", 100)
	if err != nil {
		t.Fatalf("UpsertFolder: %v", err)
	}
	if _, err := folders.db.ExecContext(ctx, `UPDATE folders SET last_uid = 42 WHERE id = ?`, real.ID); err != nil {
		t.Fatalf("simulating advanced last_uid: %v", err)
	}

	var f *domain.Folder
	err = folders.w.Do(ctx, func(tx *sql.Tx) error {
		var err error
		f, err = folders.GetOrCreateManual(ctx, tx, accountID, "Archive")
		return err
	})
	if err != nil {
		t.Fatalf("GetOrCreateManual: %v", err)
	}
	if f.ID != real.ID {
		t.Fatalf("got a different folder (id=%d), want the existing one (id=%d)", f.ID, real.ID)
	}
	if f.UIDValidity != 100 {
		t.Errorf("UIDValidity = %d, want 100 untouched (not reset by a manual-folder lookup)", f.UIDValidity)
	}
	if f.LastUID != 42 {
		t.Errorf("LastUID = %d, want 42 untouched — GetOrCreateManual must never disturb a real folder's sync bookkeeping", f.LastUID)
	}
}

func TestFoldersRepo_GetOrCreateManual_IdempotentAcrossCalls(t *testing.T) {
	folders, accounts := openTestFoldersRepo(t)
	accountID := mustCreateAccount(t, accounts)
	ctx := context.Background()

	getOrCreate := func() *domain.Folder {
		var f *domain.Folder
		err := folders.w.Do(ctx, func(tx *sql.Tx) error {
			var err error
			f, err = folders.GetOrCreateManual(ctx, tx, accountID, "Reorganized")
			return err
		})
		if err != nil {
			t.Fatalf("GetOrCreateManual: %v", err)
		}
		return f
	}

	first := getOrCreate()
	second := getOrCreate()
	if first.ID != second.ID {
		t.Errorf("second call created a new folder (id=%d), want the same one (id=%d)", second.ID, first.ID)
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
