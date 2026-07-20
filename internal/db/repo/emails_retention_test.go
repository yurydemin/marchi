package repo

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/yurydemin/marchi/internal/db/writer"
	"github.com/yurydemin/marchi/internal/domain"
)

// backdateColumn directly rewrites a DATETIME column via raw SQL — Insert
// always stamps created_at via CURRENT_TIMESTAMP, so tests that need a
// specific created_at to exercise cutoff comparisons have to reach around
// that (MarkMovedToS3Only's s3_only_since, by contrast, takes an explicit
// now parameter and can just be passed the desired value directly).
func backdateColumn(t *testing.T, sqlDB *sql.DB, table, column string, id int64, when time.Time) {
	t.Helper()
	_, err := sqlDB.Exec(`UPDATE `+table+` SET `+column+` = ? WHERE id = ?`,
		when.UTC().Format(sqliteTimeFormat), id)
	if err != nil {
		t.Fatalf("backdating %s.%s: %v", table, column, err)
	}
}

func insertTestEmail(t *testing.T, emails *EmailsRepo, w writer.Writer, accountID, folderID int64, uid uint32, storageLocation string, s3Etag string) int64 {
	t.Helper()
	var id int64
	err := w.Do(context.Background(), func(tx *sql.Tx) error {
		var err error
		id, err = emails.Insert(context.Background(), tx, &domain.Email{
			MessageID: "retention-test@example.com", AccountID: accountID, FolderID: folderID, UID: uid,
			StorageLocation: storageLocation, LocalPath: "/x",
		})
		return err
	})
	if err != nil {
		t.Fatalf("inserting test email: %v", err)
	}
	if s3Etag != "" {
		if _, err := emails.db.Exec(`UPDATE emails SET s3_etag = ? WHERE id = ?`, s3Etag, id); err != nil {
			t.Fatalf("setting s3_etag: %v", err)
		}
	}
	return id
}

func TestEmailsRepo_ListLocalDueForS3Eviction_RequiresConfirmedUploadAndCutoff(t *testing.T) {
	emails, folders, accounts, w := openTestEmailsRepo(t)
	ctx := context.Background()

	accountID := mustCreateAccount(t, accounts)
	folder := mustCreateFolder(t, folders, accountID, "INBOX")

	now := time.Now().UTC()
	old := now.Add(-30 * 24 * time.Hour)

	// Old + confirmed upload: due for eviction.
	dueID := insertTestEmail(t, emails, w, accountID, folder.ID, 1, "local", "etag-1")
	backdateColumn(t, emails.db, "emails", "created_at", dueID, old)

	// Old but NOT yet confirmed uploaded (s3_etag still NULL): must NOT
	// be evicted even though it's past the cutoff — brief.md §4.6.
	notConfirmedID := insertTestEmail(t, emails, w, accountID, folder.ID, 2, "local", "")
	backdateColumn(t, emails.db, "emails", "created_at", notConfirmedID, old)

	// Confirmed but too recent: not due yet.
	insertTestEmail(t, emails, w, accountID, folder.ID, 3, "local", "etag-3")

	cutoff := now.Add(-7 * 24 * time.Hour)
	due, err := emails.ListLocalDueForS3Eviction(ctx, accountID, cutoff)
	if err != nil {
		t.Fatalf("ListLocalDueForS3Eviction: %v", err)
	}
	if len(due) != 1 || due[0].ID != dueID {
		t.Errorf("due = %+v, want exactly [id=%d]", due, dueID)
	}
}

func TestEmailsRepo_ListLocalDueForDirectDelete_IgnoresS3EtagState(t *testing.T) {
	emails, folders, accounts, w := openTestEmailsRepo(t)
	ctx := context.Background()

	accountID := mustCreateAccount(t, accounts)
	folder := mustCreateFolder(t, folders, accountID, "INBOX")

	now := time.Now().UTC()
	old := now.Add(-30 * 24 * time.Hour)

	id := insertTestEmail(t, emails, w, accountID, folder.ID, 1, "local", "") // no S3 backup at all
	backdateColumn(t, emails.db, "emails", "created_at", id, old)

	cutoff := now.Add(-7 * 24 * time.Hour)
	due, err := emails.ListLocalDueForDirectDelete(ctx, accountID, cutoff)
	if err != nil {
		t.Fatalf("ListLocalDueForDirectDelete: %v", err)
	}
	if len(due) != 1 || due[0].ID != id {
		t.Errorf("due = %+v, want exactly [id=%d]", due, id)
	}
}

func TestEmailsRepo_MarkMovedToS3Only_ThenListS3OnlyDueForDeletion(t *testing.T) {
	emails, folders, accounts, w := openTestEmailsRepo(t)
	ctx := context.Background()

	accountID := mustCreateAccount(t, accounts)
	folder := mustCreateFolder(t, folders, accountID, "INBOX")
	id := insertTestEmail(t, emails, w, accountID, folder.ID, 1, "local", "etag-1")

	if err := w.Do(ctx, func(tx *sql.Tx) error {
		return emails.MarkMovedToS3Only(ctx, tx, id, time.Now())
	}); err != nil {
		t.Fatalf("MarkMovedToS3Only: %v", err)
	}

	got, err := emails.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.StorageLocation != "s3" || got.LocalPath != "" {
		t.Errorf("got = %+v, want storage_location=s3 and cleared LocalPath", got)
	}
	if got.S3OnlySince.IsZero() {
		t.Error("S3OnlySince is zero, want a timestamp")
	}

	now := time.Now().UTC()
	old := now.Add(-3000 * 24 * time.Hour) // ~8 years, well past the 2555-day (7yr) default
	backdateColumn(t, emails.db, "emails", "s3_only_since", id, old)

	cutoff := now.Add(-2555 * 24 * time.Hour)
	due, err := emails.ListS3OnlyDueForDeletion(ctx, accountID, cutoff)
	if err != nil {
		t.Fatalf("ListS3OnlyDueForDeletion: %v", err)
	}
	if len(due) != 1 || due[0].ID != id {
		t.Errorf("due = %+v, want exactly [id=%d]", due, id)
	}
}

func TestEmailsRepo_DeleteCompletely_RemovesRow(t *testing.T) {
	emails, folders, accounts, w := openTestEmailsRepo(t)
	ctx := context.Background()

	accountID := mustCreateAccount(t, accounts)
	folder := mustCreateFolder(t, folders, accountID, "INBOX")
	id := insertTestEmail(t, emails, w, accountID, folder.ID, 1, "s3", "etag-1")

	if err := w.Do(ctx, func(tx *sql.Tx) error {
		return emails.DeleteCompletely(ctx, tx, id)
	}); err != nil {
		t.Fatalf("DeleteCompletely: %v", err)
	}

	if _, err := emails.GetByID(ctx, id); err == nil {
		t.Error("GetByID after DeleteCompletely succeeded, want an error")
	}
}
