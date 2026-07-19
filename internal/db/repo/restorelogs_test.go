package repo

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/yurydemin/marchi/internal/db"
	"github.com/yurydemin/marchi/internal/db/writer"
	"github.com/yurydemin/marchi/internal/domain"
)

func openTestRestoreLogsRepo(t *testing.T) (*RestoreLogsRepo, *EmailsRepo, *FoldersRepo, *AccountsRepo, writer.Writer) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mailvault.db")
	sqlDB, err := db.Open(path)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })

	w := writer.New(sqlDB)
	t.Cleanup(func() { w.Close() })

	return NewRestoreLogsRepo(sqlDB, w), NewEmailsRepo(sqlDB, w), NewFoldersRepo(sqlDB, w), NewAccountsRepo(sqlDB, w), w
}

// insertEmail inserts one email under accountID/folderID with the given
// uid — used instead of mustCreateEmail when a test needs several emails
// under the same account (mustCreateEmail creates its own fixed-email
// account fixture every call, which conflicts on a second call).
func insertEmail(t *testing.T, emails *EmailsRepo, w writer.Writer, accountID, folderID int64, uid uint32) int64 {
	t.Helper()
	var id int64
	err := w.Do(context.Background(), func(tx *sql.Tx) error {
		var err error
		id, err = emails.Insert(context.Background(), tx, &domain.Email{
			MessageID: fmt.Sprintf("restore-test-%d@example.com", uid), AccountID: accountID, FolderID: folderID, UID: uid,
			StorageLocation: "local", LocalPath: "/tmp/does-not-matter.eml",
		})
		return err
	})
	if err != nil {
		t.Fatalf("inserting email uid=%d: %v", uid, err)
	}
	return id
}

func TestRestoreLogsRepo_CreateAndListByEmail(t *testing.T) {
	logs, emails, folders, accounts, w := openTestRestoreLogsRepo(t)
	ctx := context.Background()

	accountID := mustCreateAccount(t, accounts)
	folder := mustCreateFolder(t, folders, accountID, "INBOX")
	emailID := insertEmail(t, emails, w, accountID, folder.ID, 1)

	id, err := logs.Create(ctx, &domain.RestoreLog{
		EmailID: emailID, TargetAccountID: accountID, TargetFolder: "INBOX",
		Method: domain.RestoreMethodIMAPAppend, Status: domain.RestoreStatusCompleted,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if id == 0 {
		t.Fatal("Create returned id=0")
	}

	if _, err := logs.Create(ctx, &domain.RestoreLog{
		EmailID: emailID, TargetAccountID: accountID, TargetFolder: "INBOX",
		Method: domain.RestoreMethodSMTP, Status: domain.RestoreStatusFailed, ErrorMsg: "connection refused",
	}); err != nil {
		t.Fatalf("second Create: %v", err)
	}

	got, err := logs.ListByEmail(ctx, emailID)
	if err != nil {
		t.Fatalf("ListByEmail: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d logs, want 2", len(got))
	}
	// Newest first.
	if got[0].Method != domain.RestoreMethodSMTP || got[0].Status != domain.RestoreStatusFailed || got[0].ErrorMsg != "connection refused" {
		t.Errorf("got[0] = %+v, want the failed SMTP attempt", got[0])
	}
	if got[1].Method != domain.RestoreMethodIMAPAppend || got[1].Status != domain.RestoreStatusCompleted {
		t.Errorf("got[1] = %+v, want the completed APPEND attempt", got[1])
	}
	if got[1].CreatedAt.IsZero() {
		t.Error("CreatedAt is zero, want a timestamp")
	}
}

func TestRestoreLogsRepo_ListRecent_AcrossEmails(t *testing.T) {
	logs, emails, folders, accounts, w := openTestRestoreLogsRepo(t)
	ctx := context.Background()

	accountID := mustCreateAccount(t, accounts)
	folder := mustCreateFolder(t, folders, accountID, "INBOX")
	email1 := insertEmail(t, emails, w, accountID, folder.ID, 1)
	email2 := insertEmail(t, emails, w, accountID, folder.ID, 2)

	for _, emailID := range []int64{email1, email2} {
		if _, err := logs.Create(ctx, &domain.RestoreLog{
			EmailID: emailID, TargetAccountID: accountID, TargetFolder: "INBOX",
			Method: domain.RestoreMethodIMAPAppend, Status: domain.RestoreStatusCompleted,
		}); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	got, err := logs.ListRecent(ctx, 10)
	if err != nil {
		t.Fatalf("ListRecent: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d logs, want 2", len(got))
	}
}

func TestRestoreLogsRepo_ListRecent_RespectsLimit(t *testing.T) {
	logs, emails, folders, accounts, w := openTestRestoreLogsRepo(t)
	ctx := context.Background()

	accountID := mustCreateAccount(t, accounts)
	folder := mustCreateFolder(t, folders, accountID, "INBOX")
	for i := uint32(1); i <= 5; i++ {
		emailID := insertEmail(t, emails, w, accountID, folder.ID, i)
		if _, err := logs.Create(ctx, &domain.RestoreLog{
			EmailID: emailID, TargetAccountID: accountID, TargetFolder: "INBOX",
			Method: domain.RestoreMethodIMAPAppend, Status: domain.RestoreStatusCompleted,
		}); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	got, err := logs.ListRecent(ctx, 2)
	if err != nil {
		t.Fatalf("ListRecent: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d logs, want 2 (limit)", len(got))
	}
}
