package s3store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yurydemin/marchi/internal/db"
	"github.com/yurydemin/marchi/internal/db/repo"
	"github.com/yurydemin/marchi/internal/db/writer"
	"github.com/yurydemin/marchi/internal/domain"
)

func TestBackoff(t *testing.T) {
	cases := []struct {
		retryCount int
		want       time.Duration
	}{
		{0, backoffBase},
		{1, backoffBase},
		{2, 4 * time.Second},
		{3, 8 * time.Second},
		{20, maxBackoff}, // 2^19 * 2s far exceeds the 1h cap
	}
	for _, c := range cases {
		if got := backoff(c.retryCount); got != c.want {
			t.Errorf("backoff(%d) = %v, want %v", c.retryCount, got, c.want)
		}
	}
}

// TestUploader_ProcessesQueueAgainstRealMinIO is Phase 3 step 7's demo
// criterion: sync a queued email through the worker pool against a real
// MinIO container and confirm it lands in S3 with s3_etag/s3_sha256
// recorded and the queue row removed.
func TestUploader_ProcessesQueueAgainstRealMinIO(t *testing.T) {
	sqlPath := filepath.Join(t.TempDir(), "marchi.db")
	sqlDB, err := db.Open(sqlPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer sqlDB.Close()
	w := writer.New(sqlDB)
	defer w.Close()

	accountsRepo := repo.NewAccountsRepo(sqlDB, w)
	foldersRepo := repo.NewFoldersRepo(sqlDB, w)
	emailsRepo := repo.NewEmailsRepo(sqlDB, w)
	queueRepo := repo.NewS3UploadQueueRepo(sqlDB, w)
	ctx := context.Background()

	accountID, err := accountsRepo.Create(ctx, &domain.Account{
		Email: "uploader-test@example.com", IMAPHost: "imap.example.com", IMAPPort: 993,
		IMAPTLS: domain.IMAPTLSSSL, IMAPPasswordEncrypted: []byte("ct"),
	})
	if err != nil {
		t.Fatalf("creating account: %v", err)
	}
	folder, err := foldersRepo.UpsertFolder(ctx, accountID, "INBOX", 1)
	if err != nil {
		t.Fatalf("UpsertFolder: %v", err)
	}

	eml := []byte("From: a@example.com\r\nSubject: uploader test\r\n\r\nbody content")
	localPath := filepath.Join(t.TempDir(), "1.eml")
	if err := os.WriteFile(localPath, eml, 0o644); err != nil {
		t.Fatalf("writing local .eml: %v", err)
	}

	var emailID int64
	err = w.Do(ctx, func(tx *sql.Tx) error {
		var err error
		emailID, err = emailsRepo.Insert(ctx, tx, &domain.Email{
			MessageID: "uploader-test@example.com", AccountID: accountID, FolderID: folder.ID, UID: 1,
			StorageLocation: "local", LocalPath: localPath,
		})
		if err != nil {
			return err
		}
		key := EmailKey(accountID, time.Now().UTC(), ContentSHA256Hex(eml))
		return queueRepo.Enqueue(ctx, tx, emailID, key, localPath)
	})
	if err != nil {
		t.Fatalf("seeding email + queue row: %v", err)
	}

	c := newTestClient(t)
	masterKey := randMasterKey(t)

	uploader := NewUploader(UploaderDeps{
		Client:     c,
		QueueRepo:  queueRepo,
		MasterKey:  masterKey,
		Workers:    2,
		PollPeriod: 100 * time.Millisecond,
	})
	uploaderCtx, cancel := context.WithCancel(ctx)
	uploader.Start(uploaderCtx)
	defer func() {
		cancel()
		uploader.Stop()
	}()

	deadline := time.Now().Add(10 * time.Second)
	var email *domain.Email
	for time.Now().Before(deadline) {
		email, err = emailsRepo.GetByID(ctx, emailID)
		if err != nil {
			t.Fatalf("GetByID: %v", err)
		}
		if email.S3ETag != "" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	if email.S3ETag == "" {
		t.Fatal("email.S3ETag was never set — upload did not complete in time")
	}
	if email.S3SHA256 != ContentSHA256Hex(eml) {
		t.Errorf("email.S3SHA256 = %q, want %q", email.S3SHA256, ContentSHA256Hex(eml))
	}

	counts, err := queueRepo.CountByStatus(ctx)
	if err != nil {
		t.Fatalf("CountByStatus: %v", err)
	}
	if len(counts) != 0 {
		t.Errorf("queue still has rows after successful upload: %+v", counts)
	}

	// Confirm the object is actually retrievable and decrypts back to the
	// original content — not just that SQLite bookkeeping looks right.
	body, meta, err := c.Get(ctx, email.S3Key)
	if err != nil {
		t.Fatalf("Get uploaded object: %v", err)
	}
	defer body.Close()
	downloaded := make([]byte, 0)
	buf := make([]byte, 4096)
	for {
		n, rerr := body.Read(buf)
		downloaded = append(downloaded, buf[:n]...)
		if rerr != nil {
			break
		}
	}
	got, err := DecryptObject(masterKey, downloaded, meta)
	if err != nil {
		t.Fatalf("DecryptObject: %v", err)
	}
	if string(got) != string(eml) {
		t.Errorf("decrypted uploaded content = %q, want %q", got, eml)
	}
}
