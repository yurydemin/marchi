package repo

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/yurydemin/marchi/internal/db"
	"github.com/yurydemin/marchi/internal/db/writer"
	"github.com/yurydemin/marchi/internal/domain"
)

func openTestS3UploadQueueRepo(t *testing.T) (*S3UploadQueueRepo, *EmailsRepo, *FoldersRepo, *AccountsRepo, writer.Writer) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mailvault.db")
	sqlDB, err := db.Open(path)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })

	w := writer.New(sqlDB)
	t.Cleanup(func() { w.Close() })

	return NewS3UploadQueueRepo(sqlDB, w), NewEmailsRepo(sqlDB, w), NewFoldersRepo(sqlDB, w), NewAccountsRepo(sqlDB, w), w
}

// mustCreateEmail seeds an account/folder/email row so a queue row's
// email_id foreign key has something valid to point at (foreign_keys=ON,
// поправка #11).
func mustCreateEmail(t *testing.T, emails *EmailsRepo, folders *FoldersRepo, accounts *AccountsRepo, w writer.Writer, uid uint32) int64 {
	t.Helper()
	ctx := context.Background()
	accountID := mustCreateAccount(t, accounts)
	folder := mustCreateFolder(t, folders, accountID, "INBOX")

	var emailID int64
	err := w.Do(ctx, func(tx *sql.Tx) error {
		var err error
		emailID, err = emails.Insert(ctx, tx, &domain.Email{
			MessageID: "queue-test@example.com", AccountID: accountID, FolderID: folder.ID, UID: uid,
			StorageLocation: "local", LocalPath: "/tmp/does-not-matter.eml",
		})
		return err
	})
	if err != nil {
		t.Fatalf("seeding email: %v", err)
	}
	return emailID
}

func TestS3UploadQueueRepo_EnqueueAndClaimBatch(t *testing.T) {
	q, emails, folders, accounts, w := openTestS3UploadQueueRepo(t)
	ctx := context.Background()
	emailID := mustCreateEmail(t, emails, folders, accounts, w, 1)

	if err := w.Do(ctx, func(tx *sql.Tx) error {
		return q.Enqueue(ctx, tx, emailID, "mailvault/v1/accounts/1/emails/2026/07/19/ab/abcd.eml", "/data/maildir/1/INBOX/new/abcd")
	}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	claimed, err := q.ClaimBatch(ctx, 10)
	if err != nil {
		t.Fatalf("ClaimBatch: %v", err)
	}
	if len(claimed) != 1 {
		t.Fatalf("got %d claimed items, want 1", len(claimed))
	}
	if claimed[0].EmailID != emailID {
		t.Errorf("EmailID = %d, want %d", claimed[0].EmailID, emailID)
	}
	if claimed[0].Status != domain.S3QueueStatusUploading {
		t.Errorf("Status after claim = %q, want %q", claimed[0].Status, domain.S3QueueStatusUploading)
	}

	// A second claim must not pick up the same (now 'uploading') row.
	claimedAgain, err := q.ClaimBatch(ctx, 10)
	if err != nil {
		t.Fatalf("second ClaimBatch: %v", err)
	}
	if len(claimedAgain) != 0 {
		t.Errorf("second ClaimBatch claimed %d rows, want 0 (already claimed)", len(claimedAgain))
	}
}

func TestS3UploadQueueRepo_ClaimBatch_RespectsNextAttemptAt(t *testing.T) {
	q, emails, folders, accounts, w := openTestS3UploadQueueRepo(t)
	ctx := context.Background()
	emailID := mustCreateEmail(t, emails, folders, accounts, w, 1)

	if err := w.Do(ctx, func(tx *sql.Tx) error {
		return q.Enqueue(ctx, tx, emailID, "key", "/path")
	}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	claimed, err := q.ClaimBatch(ctx, 10)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("initial ClaimBatch: claimed=%d err=%v", len(claimed), err)
	}

	// Simulate a failed attempt scheduled an hour in the future.
	if err := q.MarkFailed(ctx, claimed[0].ID, 1, 5, "network error", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}

	notYetDue, err := q.ClaimBatch(ctx, 10)
	if err != nil {
		t.Fatalf("ClaimBatch after MarkFailed: %v", err)
	}
	if len(notYetDue) != 0 {
		t.Errorf("ClaimBatch before next_attempt_at claimed %d rows, want 0", len(notYetDue))
	}
}

func TestS3UploadQueueRepo_MarkDone_UpdatesEmailAndRemovesRow(t *testing.T) {
	q, emails, folders, accounts, w := openTestS3UploadQueueRepo(t)
	ctx := context.Background()
	emailID := mustCreateEmail(t, emails, folders, accounts, w, 1)

	if err := w.Do(ctx, func(tx *sql.Tx) error {
		return q.Enqueue(ctx, tx, emailID, "mailvault/v1/accounts/1/emails/k.eml", "/path")
	}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	claimed, err := q.ClaimBatch(ctx, 10)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("ClaimBatch: claimed=%d err=%v", len(claimed), err)
	}

	if err := q.MarkDone(ctx, claimed[0], "etag-123", "deadbeef"); err != nil {
		t.Fatalf("MarkDone: %v", err)
	}

	email, err := emails.GetByID(ctx, emailID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if email.S3ETag != "etag-123" || email.S3SHA256 != "deadbeef" || email.S3Key != "mailvault/v1/accounts/1/emails/k.eml" {
		t.Errorf("email S3 fields = %+v, want etag/sha256/key set from MarkDone", email)
	}

	remaining, err := q.ClaimBatch(ctx, 10)
	if err != nil {
		t.Fatalf("ClaimBatch after MarkDone: %v", err)
	}
	if len(remaining) != 0 {
		t.Errorf("queue still has %d rows after MarkDone, want 0", len(remaining))
	}
}

func TestS3UploadQueueRepo_MarkFailed_TerminatesAfterMaxAttempts(t *testing.T) {
	q, emails, folders, accounts, w := openTestS3UploadQueueRepo(t)
	ctx := context.Background()
	emailID := mustCreateEmail(t, emails, folders, accounts, w, 1)

	if err := w.Do(ctx, func(tx *sql.Tx) error {
		return q.Enqueue(ctx, tx, emailID, "key", "/path")
	}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	claimed, _ := q.ClaimBatch(ctx, 10)

	if err := q.MarkFailed(ctx, claimed[0].ID, 5, 5, "persistent failure", time.Now()); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}

	counts, err := q.CountByStatus(ctx)
	if err != nil {
		t.Fatalf("CountByStatus: %v", err)
	}
	if counts[domain.S3QueueStatusFailed] != 1 {
		t.Errorf("CountByStatus[failed] = %d, want 1 (retryCount reached maxAttempts): %+v", counts[domain.S3QueueStatusFailed], counts)
	}
}
