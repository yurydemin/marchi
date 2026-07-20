package repo

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/yurydemin/marchi/internal/db"
	"github.com/yurydemin/marchi/internal/db/writer"
	"github.com/yurydemin/marchi/internal/domain"
)

type attachmentsTestEnv struct {
	accounts    *AccountsRepo
	folders     *FoldersRepo
	emails      *EmailsRepo
	attachments *AttachmentsRepo
	w           writer.Writer
}

func openAttachmentsTestEnv(t *testing.T) *attachmentsTestEnv {
	t.Helper()
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "marchi.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	w := writer.New(sqlDB)
	t.Cleanup(func() { w.Close() })

	return &attachmentsTestEnv{
		accounts:    NewAccountsRepo(sqlDB, w),
		folders:     NewFoldersRepo(sqlDB, w),
		emails:      NewEmailsRepo(sqlDB, w),
		attachments: NewAttachmentsRepo(sqlDB, w),
		w:           w,
	}
}

func (env *attachmentsTestEnv) createEmail(t *testing.T, accountID, folderID int64, uid uint32) int64 {
	t.Helper()
	var id int64
	err := env.w.Do(context.Background(), func(tx *sql.Tx) error {
		var err error
		id, err = env.emails.Insert(context.Background(), tx, &domain.Email{
			MessageID: "x@example.com", AccountID: accountID, FolderID: folderID,
			UID: uid, StorageLocation: "local", LocalPath: "/x",
		})
		return err
	})
	if err != nil {
		t.Fatalf("creating email fixture: %v", err)
	}
	return id
}

func TestAttachmentsRepo_InsertAndListByEmail(t *testing.T) {
	env := openAttachmentsTestEnv(t)
	ctx := context.Background()

	accountID := mustCreateAccount(t, env.accounts)
	folder := mustCreateFolder(t, env.folders, accountID, "INBOX")
	emailID := env.createEmail(t, accountID, folder.ID, 1)

	err := env.w.Do(ctx, func(tx *sql.Tx) error {
		if _, err := env.attachments.Insert(ctx, tx, &domain.Attachment{
			EmailID: emailID, Filename: "report.pdf", MIMEType: "application/pdf", Size: 12345,
		}); err != nil {
			return err
		}
		_, err := env.attachments.Insert(ctx, tx, &domain.Attachment{
			EmailID: emailID, Filename: "logo.png", MIMEType: "image/png", Size: 999, ContentID: "logo123",
		})
		return err
	})
	if err != nil {
		t.Fatalf("inserting attachments: %v", err)
	}

	list, err := env.attachments.ListByEmail(ctx, emailID)
	if err != nil {
		t.Fatalf("ListByEmail: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("got %d attachments, want 2", len(list))
	}
	if list[0].Filename != "report.pdf" || list[0].MIMEType != "application/pdf" || list[0].Size != 12345 {
		t.Errorf("list[0] = %+v", list[0])
	}
	if list[1].Filename != "logo.png" || list[1].ContentID != "logo123" {
		t.Errorf("list[1] = %+v", list[1])
	}
}

func TestAttachmentsRepo_EmailWithNoAttachments(t *testing.T) {
	env := openAttachmentsTestEnv(t)
	ctx := context.Background()

	accountID := mustCreateAccount(t, env.accounts)
	folder := mustCreateFolder(t, env.folders, accountID, "INBOX")
	emailID := env.createEmail(t, accountID, folder.ID, 1)

	list, err := env.attachments.ListByEmail(ctx, emailID)
	if err != nil {
		t.Fatalf("ListByEmail: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("got %d attachments, want 0", len(list))
	}
}

func TestAttachmentsRepo_CascadeDeletedWithEmail(t *testing.T) {
	env := openAttachmentsTestEnv(t)
	ctx := context.Background()

	accountID := mustCreateAccount(t, env.accounts)
	folder := mustCreateFolder(t, env.folders, accountID, "INBOX")
	emailID := env.createEmail(t, accountID, folder.ID, 1)

	if err := env.w.Do(ctx, func(tx *sql.Tx) error {
		_, err := env.attachments.Insert(ctx, tx, &domain.Attachment{EmailID: emailID, Filename: "f.pdf"})
		return err
	}); err != nil {
		t.Fatal(err)
	}

	if err := env.w.Do(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM emails WHERE id = ?`, emailID)
		return err
	}); err != nil {
		t.Fatalf("deleting email: %v", err)
	}

	list, err := env.attachments.ListByEmail(ctx, emailID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Errorf("expected attachments to cascade-delete with parent email, got %d remaining", len(list))
	}
}
