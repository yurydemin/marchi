package repo

import (
	"context"
	"database/sql"
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
	path := filepath.Join(t.TempDir(), "mailvault.db")
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
