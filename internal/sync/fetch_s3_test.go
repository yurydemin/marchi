package sync

import (
	"context"
	"testing"
	"time"

	"github.com/yurydemin/marchi/internal/db/repo"
	"github.com/yurydemin/marchi/internal/domain"
	"github.com/yurydemin/marchi/internal/s3store"
	"github.com/yurydemin/marchi/internal/testutil/minio"
)

// TestFetchNewMessages_S3Mirror_EnqueuesAndUploadsToRealMinIO is Phase 3
// step 7's demo criterion: sync a test mailbox with S3 mirroring enabled
// and confirm the archived email is actually in S3 — archiveOne enqueues
// it (FR-S3-03), and internal/s3store.Uploader drains the queue against a
// real MinIO container.
func TestFetchNewMessages_S3Mirror_EnqueuesAndUploadsToRealMinIO(t *testing.T) {
	env := newFetchTestEnv(t)
	ctx := context.Background()

	addr := startFakeFetchServer(t, fakeFetchServer{
		uidValidity: 1001,
		uidNext:     2,
		messages: []fakeMessage{
			{uid: 1, flags: "", body: testEmail("S3 mirror test")},
		},
	})
	c := connectToFakeServer(t, addr)
	defer c.Logout()

	folder, err := env.foldersR.UpsertFolder(ctx, env.accountID, "INBOX", 1001)
	if err != nil {
		t.Fatalf("UpsertFolder: %v", err)
	}
	mw := env.newWriter(t, "INBOX")

	srv := minio.Start(t)
	client, err := s3store.NewClient(s3store.Options{
		Endpoint: srv.Endpoint, Region: "us-east-1", Bucket: "marchi-sync-test",
		AccessKeyID: srv.AccessKeyID, SecretAccessKey: srv.SecretKey, PathStyle: true,
	})
	if err != nil {
		t.Fatalf("s3store.NewClient: %v", err)
	}
	if err := client.EnsureBucket(ctx); err != nil {
		t.Fatalf("EnsureBucket: %v", err)
	}

	queueRepo := repo.NewS3UploadQueueRepo(env.sqlDB, env.w)

	stats, err := FetchNewMessages(ctx, c, env.accountID, folder, mw, env.w, env.emailsR, env.foldersR, env.attachmentsR, nil, queueRepo, nil, nil)
	if err != nil {
		t.Fatalf("FetchNewMessages: %v", err)
	}
	if stats.Archived != 1 {
		t.Fatalf("Archived = %d, want 1", stats.Archived)
	}

	counts, err := queueRepo.CountByStatus(ctx)
	if err != nil {
		t.Fatalf("CountByStatus: %v", err)
	}
	if counts[domain.S3QueueStatusPending] != 1 {
		t.Fatalf("queue counts after archiving = %+v, want 1 pending row", counts)
	}

	masterKey := randMasterKeyForTest(t)
	uploader := s3store.NewUploader(s3store.UploaderDeps{
		Client: client, QueueRepo: queueRepo, MasterKey: masterKey,
		Workers: 2, PollPeriod: 100 * time.Millisecond,
	})
	uploaderCtx, cancel := context.WithCancel(ctx)
	uploader.Start(uploaderCtx)
	defer func() {
		cancel()
		uploader.Stop()
	}()

	emails, err := env.emailsR.ListByFolder(ctx, folder.ID)
	if err != nil {
		t.Fatalf("ListByFolder: %v", err)
	}
	if len(emails) != 1 {
		t.Fatalf("got %d emails, want 1", len(emails))
	}
	emailID := emails[0].ID

	deadline := time.Now().Add(10 * time.Second)
	var email *domain.Email
	for time.Now().Before(deadline) {
		email, err = env.emailsR.GetByID(ctx, emailID)
		if err != nil {
			t.Fatalf("GetByID: %v", err)
		}
		if email.S3ETag != "" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if email.S3ETag == "" {
		t.Fatal("email.S3ETag was never set — the mirror upload did not complete in time")
	}

	body, meta, err := client.Get(ctx, email.S3Key)
	if err != nil {
		t.Fatalf("Get uploaded object %q: %v", email.S3Key, err)
	}
	defer body.Close()
	buf := make([]byte, 0)
	chunk := make([]byte, 4096)
	for {
		n, rerr := body.Read(chunk)
		buf = append(buf, chunk[:n]...)
		if rerr != nil {
			break
		}
	}
	plaintext, err := s3store.DecryptObject(masterKey, buf, meta)
	if err != nil {
		t.Fatalf("DecryptObject: %v", err)
	}
	if string(plaintext) != string(testEmail("S3 mirror test")) {
		t.Errorf("uploaded content mismatch: got %q", plaintext)
	}
}

func randMasterKeyForTest(t *testing.T) []byte {
	t.Helper()
	return []byte("01234567890123456789012345678901")[:32]
}
