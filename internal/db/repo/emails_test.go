package repo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/yurydemin/marchi/internal/db"
	"github.com/yurydemin/marchi/internal/db/writer"
	"github.com/yurydemin/marchi/internal/domain"
)

func openTestEmailsRepo(t *testing.T) (*EmailsRepo, *FoldersRepo, *AccountsRepo, writer.Writer) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "marchi.db")
	sqlDB, err := db.Open(path)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })

	w := writer.New(sqlDB)
	t.Cleanup(func() { w.Close() })

	return NewEmailsRepo(sqlDB, w), NewFoldersRepo(sqlDB, w), NewAccountsRepo(sqlDB, w), w
}

func mustCreateFolder(t *testing.T, folders *FoldersRepo, accountID int64, name string) *domain.Folder {
	t.Helper()
	f, err := folders.UpsertFolder(context.Background(), accountID, name, 1)
	if err != nil {
		t.Fatalf("UpsertFolder: %v", err)
	}
	return f
}

func TestEmailsRepo_InsertAndListByFolder(t *testing.T) {
	emails, folders, accounts, w := openTestEmailsRepo(t)
	ctx := context.Background()

	accountID := mustCreateAccount(t, accounts)
	folder := mustCreateFolder(t, folders, accountID, "INBOX")

	when := time.Date(2026, 3, 15, 10, 30, 0, 0, time.UTC)
	var emailID int64
	err := w.Do(ctx, func(tx *sql.Tx) error {
		var err error
		emailID, err = emails.Insert(ctx, tx, &domain.Email{
			MessageID:       "abc@example.com",
			AccountID:       accountID,
			FolderID:        folder.ID,
			UID:             1,
			Subject:         "Hello",
			FromAddr:        "a@example.com",
			ToAddrs:         []string{"b@example.com", "c@example.com"},
			CcAddrs:         []string{"d@example.com"},
			Date:            when,
			Size:            1234,
			HasAttachments:  false,
			Flags:           []string{"\\Seen"},
			StorageLocation: "local",
			LocalPath:       "/data/maildir/accounts/1/mail/INBOX/new/123.eml",
		})
		return err
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if emailID == 0 {
		t.Fatal("expected a non-zero email ID")
	}

	list, err := emails.ListByFolder(ctx, folder.ID)
	if err != nil {
		t.Fatalf("ListByFolder: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("got %d emails, want 1", len(list))
	}

	e := list[0]
	if e.MessageID != "abc@example.com" {
		t.Errorf("MessageID = %q", e.MessageID)
	}
	if e.Subject != "Hello" {
		t.Errorf("Subject = %q", e.Subject)
	}
	if len(e.ToAddrs) != 2 || e.ToAddrs[0] != "b@example.com" {
		t.Errorf("ToAddrs = %v", e.ToAddrs)
	}
	if len(e.CcAddrs) != 1 {
		t.Errorf("CcAddrs = %v", e.CcAddrs)
	}
	if !e.Date.Equal(when) {
		t.Errorf("Date = %v, want %v", e.Date, when)
	}
	if e.Size != 1234 {
		t.Errorf("Size = %d", e.Size)
	}
	if len(e.Flags) != 1 || e.Flags[0] != "\\Seen" {
		t.Errorf("Flags = %v", e.Flags)
	}
	if e.LocalPath != "/data/maildir/accounts/1/mail/INBOX/new/123.eml" {
		t.Errorf("LocalPath = %q", e.LocalPath)
	}
	if e.StorageLocation != "local" {
		t.Errorf("StorageLocation = %q", e.StorageLocation)
	}
}

func TestEmailsRepo_ZeroDateStoredAsNull(t *testing.T) {
	emails, folders, accounts, w := openTestEmailsRepo(t)
	ctx := context.Background()

	accountID := mustCreateAccount(t, accounts)
	folder := mustCreateFolder(t, folders, accountID, "INBOX")

	err := w.Do(ctx, func(tx *sql.Tx) error {
		_, err := emails.Insert(ctx, tx, &domain.Email{
			MessageID: "no-date@example.com", AccountID: accountID, FolderID: folder.ID,
			UID: 1, StorageLocation: "local", LocalPath: "/x",
		})
		return err
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	list, err := emails.ListByFolder(ctx, folder.ID)
	if err != nil {
		t.Fatalf("ListByFolder: %v", err)
	}
	if !list[0].Date.IsZero() {
		t.Errorf("Date = %v, want zero value round-tripped through NULL", list[0].Date)
	}
}

func TestEmailsRepo_InsertAndUpdateLastUID_SameTransaction(t *testing.T) {
	emails, folders, accounts, w := openTestEmailsRepo(t)
	ctx := context.Background()

	accountID := mustCreateAccount(t, accounts)
	folder := mustCreateFolder(t, folders, accountID, "INBOX")

	err := w.Do(ctx, func(tx *sql.Tx) error {
		if _, err := emails.Insert(ctx, tx, &domain.Email{
			MessageID: "x@example.com", AccountID: accountID, FolderID: folder.ID,
			UID: 5, StorageLocation: "local", LocalPath: "/x",
		}); err != nil {
			return err
		}
		return folders.UpdateLastUID(ctx, tx, folder.ID, 5)
	})
	if err != nil {
		t.Fatalf("combined transaction: %v", err)
	}

	updated, err := folders.ListByAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if updated[0].LastUID != 5 {
		t.Errorf("LastUID = %d, want 5", updated[0].LastUID)
	}
}

func TestEmailsRepo_InsertFailure_DoesNotAdvanceLastUID(t *testing.T) {
	// A failed email insert must roll back the whole transaction, including
	// any last_uid bump attempted alongside it — that's the entire point of
	// doing both in one writer.Do call. Force the failure via the
	// UNIQUE(account_id, folder_id, uid) constraint: insert UID 9 once
	// successfully, then try to insert it again combined with a last_uid
	// bump, and confirm the bump didn't take either.
	emails, folders, accounts, w := openTestEmailsRepo(t)
	ctx := context.Background()

	accountID := mustCreateAccount(t, accounts)
	folder := mustCreateFolder(t, folders, accountID, "INBOX")

	err := w.Do(ctx, func(tx *sql.Tx) error {
		_, err := emails.Insert(ctx, tx, &domain.Email{
			MessageID: "first@example.com", AccountID: accountID, FolderID: folder.ID,
			UID: 9, StorageLocation: "local", LocalPath: "/x",
		})
		return err
	})
	if err != nil {
		t.Fatalf("seeding first insert: %v", err)
	}

	err = w.Do(ctx, func(tx *sql.Tx) error {
		if _, err := emails.Insert(ctx, tx, &domain.Email{
			MessageID: "duplicate-uid@example.com", AccountID: accountID, FolderID: folder.ID,
			UID: 9, StorageLocation: "local", LocalPath: "/y",
		}); err != nil {
			return err
		}
		return folders.UpdateLastUID(ctx, tx, folder.ID, 9)
	})
	if err == nil {
		t.Fatal("expected UNIQUE(account_id, folder_id, uid) violation, got nil error")
	}

	after, err := folders.ListByAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if after[0].LastUID != 0 {
		t.Errorf("LastUID = %d, want 0 (rolled back, the second insert's UpdateLastUID must not have taken)", after[0].LastUID)
	}
}

func TestEmailsRepo_ListByAccount_OnlyThatAccountAcrossFolders(t *testing.T) {
	emails, folders, accounts, w := openTestEmailsRepo(t)
	ctx := context.Background()

	accountA := mustCreateAccount(t, accounts)
	accountB, err := accounts.Create(ctx, accountFixture("other-owner@example.com"))
	if err != nil {
		t.Fatalf("creating second account fixture: %v", err)
	}
	inboxA := mustCreateFolder(t, folders, accountA, "INBOX")
	sentA := mustCreateFolder(t, folders, accountA, "Sent")
	inboxB := mustCreateFolder(t, folders, accountB, "INBOX")

	insert := func(accountID, folderID int64, uid uint32) {
		err := w.Do(ctx, func(tx *sql.Tx) error {
			_, err := emails.Insert(ctx, tx, &domain.Email{
				MessageID: fmt.Sprintf("msg-%d-%d@example.com", accountID, uid),
				AccountID: accountID, FolderID: folderID, UID: uid,
				StorageLocation: "local", LocalPath: "/x",
			})
			return err
		})
		if err != nil {
			t.Fatalf("inserting account=%d folder=%d uid=%d: %v", accountID, folderID, uid, err)
		}
	}
	insert(accountA, inboxA.ID, 1)
	insert(accountA, sentA.ID, 1)
	insert(accountB, inboxB.ID, 1)

	got, err := emails.ListByAccount(ctx, accountA)
	if err != nil {
		t.Fatalf("ListByAccount: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d emails, want 2 (accountB's email must be excluded)", len(got))
	}
	for _, e := range got {
		if e.AccountID != accountA {
			t.Errorf("email %d has AccountID = %d, want %d", e.ID, e.AccountID, accountA)
		}
	}
}

// TestEmailsRepo_ListAll_AcrossAccountsOrderedByID covers the full
// reindex path (FR-SR-04): unlike ListByAccount, ListAll must return
// every email regardless of which account archived it, in insertion
// (id) order.
func TestEmailsRepo_ListAll_AcrossAccountsOrderedByID(t *testing.T) {
	emails, folders, accounts, w := openTestEmailsRepo(t)
	ctx := context.Background()

	accountA := mustCreateAccount(t, accounts)
	accountB, err := accounts.Create(ctx, accountFixture("other-owner@example.com"))
	if err != nil {
		t.Fatalf("creating second account fixture: %v", err)
	}
	inboxA := mustCreateFolder(t, folders, accountA, "INBOX")
	inboxB := mustCreateFolder(t, folders, accountB, "INBOX")

	insert := func(accountID, folderID int64, uid uint32) int64 {
		var id int64
		err := w.Do(ctx, func(tx *sql.Tx) error {
			var err error
			id, err = emails.Insert(ctx, tx, &domain.Email{
				MessageID: fmt.Sprintf("msg-%d-%d@example.com", accountID, uid),
				AccountID: accountID, FolderID: folderID, UID: uid,
				StorageLocation: "local", LocalPath: "/x",
			})
			return err
		})
		if err != nil {
			t.Fatalf("inserting account=%d folder=%d uid=%d: %v", accountID, folderID, uid, err)
		}
		return id
	}
	first := insert(accountA, inboxA.ID, 1)
	second := insert(accountB, inboxB.ID, 1)

	got, err := emails.ListAll(ctx)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d emails, want 2 (both accounts included)", len(got))
	}
	if got[0].ID != first || got[1].ID != second {
		t.Errorf("ListAll order = [%d, %d], want [%d, %d] (insertion/id order)", got[0].ID, got[1].ID, first, second)
	}
}

func TestEmailsRepo_ListAll_Empty(t *testing.T) {
	emails, _, _, _ := openTestEmailsRepo(t)
	got, err := emails.ListAll(context.Background())
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d emails, want 0", len(got))
	}
}

func TestEmailsRepo_Stats(t *testing.T) {
	emails, folders, accounts, w := openTestEmailsRepo(t)
	ctx := context.Background()

	accountA := mustCreateAccount(t, accounts)
	accountB, err := accounts.Create(ctx, accountFixture("stats-owner@example.com"))
	if err != nil {
		t.Fatalf("creating second account: %v", err)
	}
	folderA := mustCreateFolder(t, folders, accountA, "INBOX")
	folderB := mustCreateFolder(t, folders, accountB, "INBOX")

	insert := func(accountID, folderID int64, uid uint32, size int64, storageLocation string) {
		err := w.Do(ctx, func(tx *sql.Tx) error {
			_, err := emails.Insert(ctx, tx, &domain.Email{
				MessageID: fmt.Sprintf("msg-%d-%d@example.com", accountID, uid),
				AccountID: accountID, FolderID: folderID, UID: uid,
				Size: size, StorageLocation: storageLocation, LocalPath: "/x",
			})
			return err
		})
		if err != nil {
			t.Fatalf("inserting email: %v", err)
		}
	}
	insert(accountA, folderA.ID, 1, 100, "local")
	insert(accountA, folderA.ID, 2, 200, "local")
	insert(accountB, folderB.ID, 1, 50, "s3")

	stats, err := emails.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Total != 3 {
		t.Errorf("Total = %d, want 3", stats.Total)
	}
	if stats.LocalBytes != 300 {
		t.Errorf("LocalBytes = %d, want 300", stats.LocalBytes)
	}
	if stats.S3Bytes != 50 {
		t.Errorf("S3Bytes = %d, want 50", stats.S3Bytes)
	}
	if stats.EmailsByAccount[accountA] != 2 {
		t.Errorf("EmailsByAccount[accountA] = %d, want 2", stats.EmailsByAccount[accountA])
	}
	if stats.EmailsByAccount[accountB] != 1 {
		t.Errorf("EmailsByAccount[accountB] = %d, want 1", stats.EmailsByAccount[accountB])
	}
}

func TestEmailsRepo_Stats_Empty(t *testing.T) {
	emails, _, _, _ := openTestEmailsRepo(t)
	stats, err := emails.Stats(context.Background())
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Total != 0 || stats.LocalBytes != 0 || stats.S3Bytes != 0 {
		t.Errorf("stats = %+v, want all zero", stats)
	}
	if len(stats.EmailsByAccount) != 0 {
		t.Errorf("EmailsByAccount = %v, want empty", stats.EmailsByAccount)
	}
}

func TestEmailsRepo_ListStorageLocations(t *testing.T) {
	emails, folders, accounts, w := openTestEmailsRepo(t)
	ctx := context.Background()

	accountID := mustCreateAccount(t, accounts)
	folder := mustCreateFolder(t, folders, accountID, "INBOX")

	var localID, s3ID int64
	err := w.Do(ctx, func(tx *sql.Tx) error {
		var err error
		localID, err = emails.Insert(ctx, tx, &domain.Email{
			MessageID: "local@example.com", AccountID: accountID, FolderID: folder.ID, UID: 1,
			StorageLocation: "local", LocalPath: "/data/local.eml",
		})
		if err != nil {
			return err
		}
		s3ID, err = emails.Insert(ctx, tx, &domain.Email{
			MessageID: "s3@example.com", AccountID: accountID, FolderID: folder.ID, UID: 2,
			StorageLocation: "s3",
		})
		return err
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	locations, err := emails.ListStorageLocations(ctx, []int64{localID, s3ID, 999999})
	if err != nil {
		t.Fatalf("ListStorageLocations: %v", err)
	}
	if locations[localID] != "local" {
		t.Errorf("locations[localID] = %q, want %q", locations[localID], "local")
	}
	if locations[s3ID] != "s3" {
		t.Errorf("locations[s3ID] = %q, want %q", locations[s3ID], "s3")
	}
	if _, ok := locations[999999]; ok {
		t.Error("locations should not contain an entry for a nonexistent id")
	}
}

func TestEmailsRepo_ListStorageLocations_EmptyInput(t *testing.T) {
	emails, _, _, _ := openTestEmailsRepo(t)
	locations, err := emails.ListStorageLocations(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListStorageLocations: %v", err)
	}
	if len(locations) != 0 {
		t.Errorf("locations = %v, want empty", locations)
	}
}

func TestEmailsRepo_ExistsByAccountMessageID(t *testing.T) {
	emails, folders, accounts, w := openTestEmailsRepo(t)
	ctx := context.Background()

	accountID := mustCreateAccount(t, accounts)
	otherAccountID, err := accounts.Create(ctx, accountFixture("other@example.com"))
	if err != nil {
		t.Fatalf("creating second account fixture: %v", err)
	}
	folder := mustCreateFolder(t, folders, accountID, "INBOX")

	if err := w.Do(ctx, func(tx *sql.Tx) error {
		_, err := emails.Insert(ctx, tx, &domain.Email{
			MessageID: "dup@example.com", AccountID: accountID, FolderID: folder.ID, UID: 1,
			StorageLocation: "local", LocalPath: "/data/1.eml",
		})
		return err
	}); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	exists, err := emails.ExistsByAccountMessageID(ctx, accountID, "dup@example.com")
	if err != nil {
		t.Fatalf("ExistsByAccountMessageID: %v", err)
	}
	if !exists {
		t.Error("exists = false, want true for a message_id already archived in this account")
	}

	exists, err = emails.ExistsByAccountMessageID(ctx, accountID, "never-seen@example.com")
	if err != nil {
		t.Fatalf("ExistsByAccountMessageID: %v", err)
	}
	if exists {
		t.Error("exists = true, want false for a message_id never archived")
	}

	// Scoped per account: the same Message-ID legitimately archived under
	// a different account is not a duplicate from that other account's
	// point of view.
	exists, err = emails.ExistsByAccountMessageID(ctx, otherAccountID, "dup@example.com")
	if err != nil {
		t.Fatalf("ExistsByAccountMessageID: %v", err)
	}
	if exists {
		t.Error("exists = true, want false — message_id belongs to a different account")
	}
}

// TestEmailsRepo_DeleteCompletely_CascadesRestoreLogs guards against a
// regression of migration 000008: restore_logs.email_id originally had no
// ON DELETE CASCADE, so deleting an archived email that had ever been
// restored failed outright with a FOREIGN KEY constraint error under
// PRAGMA foreign_keys=ON — exactly the path the manual "delete from
// archive" API (DELETE /api/v1/emails/{id}) exercises.
func TestEmailsRepo_NextManualMoveUID_StartsAtBaseThenIncrements(t *testing.T) {
	emails, folders, accounts, w := openTestEmailsRepo(t)
	ctx := context.Background()
	accountID := mustCreateAccount(t, accounts)
	target := mustCreateFolder(t, folders, accountID, "Reorganized")

	var first, second uint32
	err := w.Do(ctx, func(tx *sql.Tx) error {
		var err error
		first, err = emails.NextManualMoveUID(ctx, tx, target.ID)
		return err
	})
	if err != nil {
		t.Fatalf("NextManualMoveUID (empty folder): %v", err)
	}
	if first != manualMoveUIDBase {
		t.Errorf("first = %d, want %d (the base, folder has no rows yet)", first, manualMoveUIDBase)
	}

	// A real, low-numbered UID already sitting in this folder (as if it
	// were also, coincidentally, a live-synced folder) must not affect the
	// synthetic range at all.
	insertEmail(t, emails, w, accountID, target.ID, 7)

	err = w.Do(ctx, func(tx *sql.Tx) error {
		var err error
		second, err = emails.NextManualMoveUID(ctx, tx, target.ID)
		return err
	})
	if err != nil {
		t.Fatalf("NextManualMoveUID (with a real low uid present): %v", err)
	}
	if second != manualMoveUIDBase {
		t.Errorf("second = %d, want %d — a real uid=7 row must not shift the synthetic base", second, manualMoveUIDBase)
	}

	// Occupy the synthetic uid NextManualMoveUID just handed back, as if a
	// previous bulk-move had already assigned it.
	insertEmail(t, emails, w, accountID, target.ID, second)

	var third uint32
	err = w.Do(ctx, func(tx *sql.Tx) error {
		var err error
		third, err = emails.NextManualMoveUID(ctx, tx, target.ID)
		return err
	})
	if err != nil {
		t.Fatalf("NextManualMoveUID (after one synthetic uid assigned): %v", err)
	}
	if third != manualMoveUIDBase+1 {
		t.Errorf("third = %d, want %d (one past the highest synthetic uid so far)", third, manualMoveUIDBase+1)
	}
}

func TestEmailsRepo_UpdateFolderAssignment(t *testing.T) {
	emails, folders, accounts, w := openTestEmailsRepo(t)
	ctx := context.Background()
	accountID := mustCreateAccount(t, accounts)
	source := mustCreateFolder(t, folders, accountID, "INBOX")
	target := mustCreateFolder(t, folders, accountID, "Reorganized")

	emailID := insertEmail(t, emails, w, accountID, source.ID, 1)

	if err := w.Do(ctx, func(tx *sql.Tx) error {
		return emails.UpdateFolderAssignment(ctx, tx, emailID, target.ID, manualMoveUIDBase, "/data/maildir/accounts/1/mail/Reorganized/new/moved.eml")
	}); err != nil {
		t.Fatalf("UpdateFolderAssignment: %v", err)
	}

	got, err := emails.GetByID(ctx, emailID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.FolderID != target.ID {
		t.Errorf("FolderID = %d, want %d", got.FolderID, target.ID)
	}
	if got.UID != manualMoveUIDBase {
		t.Errorf("UID = %d, want %d", got.UID, manualMoveUIDBase)
	}
	if got.LocalPath != "/data/maildir/accounts/1/mail/Reorganized/new/moved.eml" {
		t.Errorf("LocalPath = %q", got.LocalPath)
	}
}

func TestEmailsRepo_UpdateFolderAssignment_EmptyLocalPathStoresNull(t *testing.T) {
	emails, folders, accounts, w := openTestEmailsRepo(t)
	ctx := context.Background()
	accountID := mustCreateAccount(t, accounts)
	source := mustCreateFolder(t, folders, accountID, "INBOX")
	target := mustCreateFolder(t, folders, accountID, "Reorganized")

	emailID := insertEmail(t, emails, w, accountID, source.ID, 1)

	if err := w.Do(ctx, func(tx *sql.Tx) error {
		return emails.UpdateFolderAssignment(ctx, tx, emailID, target.ID, manualMoveUIDBase, "")
	}); err != nil {
		t.Fatalf("UpdateFolderAssignment: %v", err)
	}

	got, err := emails.GetByID(ctx, emailID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.LocalPath != "" {
		t.Errorf("LocalPath = %q, want empty (S3-only email moved by folder, not file)", got.LocalPath)
	}
}

func TestEmailsRepo_DeleteCompletely_CascadesRestoreLogs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "marchi.db")
	sqlDB, err := db.Open(path)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	w := writer.New(sqlDB)
	t.Cleanup(func() { w.Close() })

	emails := NewEmailsRepo(sqlDB, w)
	folders := NewFoldersRepo(sqlDB, w)
	accounts := NewAccountsRepo(sqlDB, w)
	restoreLogs := NewRestoreLogsRepo(sqlDB, w)
	ctx := context.Background()

	accountID := mustCreateAccount(t, accounts)
	folder := mustCreateFolder(t, folders, accountID, "INBOX")
	emailID := insertEmail(t, emails, w, accountID, folder.ID, 1)

	if _, err := restoreLogs.Create(ctx, &domain.RestoreLog{
		EmailID: emailID, TargetAccountID: accountID, TargetFolder: "INBOX",
		Method: domain.RestoreMethodIMAPAppend, Status: domain.RestoreStatusCompleted,
	}); err != nil {
		t.Fatalf("restoreLogs.Create: %v", err)
	}

	if err := w.Do(ctx, func(tx *sql.Tx) error {
		return emails.DeleteCompletely(ctx, tx, emailID)
	}); err != nil {
		t.Fatalf("DeleteCompletely with a referencing restore_logs row: %v", err)
	}

	if _, err := emails.GetByID(ctx, emailID); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("GetByID after delete = %v, want sql.ErrNoRows", err)
	}
	logs, err := restoreLogs.ListByEmail(ctx, emailID)
	if err != nil {
		t.Fatalf("ListByEmail: %v", err)
	}
	if len(logs) != 0 {
		t.Errorf("restore_logs survived the cascade: %v", logs)
	}
}
