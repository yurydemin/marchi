package retention

import (
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"os"
	"testing"
	"time"

	"github.com/yurydemin/marchi/internal/s3config"
	"github.com/yurydemin/marchi/internal/s3store"
	"github.com/yurydemin/marchi/internal/security/crypto"
	"github.com/yurydemin/marchi/internal/testutil/minio"
)

// TestRunner_FullLifecycle_MigratesEmailThroughAllThreeStages is this
// step's demo criterion: a real email actually migrates A -> B -> C
// against a real MinIO container, driven purely by an injectable clock —
// Stage A->B (local evicted, S3-only) on the first Run once
// retention_move_to_s3_days has elapsed, then Stage B->C (deleted
// entirely, S3 object gone too) on a second Run once retention_s3_days
// has additionally elapsed since Stage B started.
func TestRunner_FullLifecycle_MigratesEmailThroughAllThreeStages(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	srv := minio.Start(t)
	client, err := s3store.NewClient(s3store.Options{
		Endpoint: srv.Endpoint, Region: "us-east-1", Bucket: "marchi-retention-test",
		AccessKeyID: srv.AccessKeyID, SecretAccessKey: srv.SecretKey, PathStyle: true,
	})
	if err != nil {
		t.Fatalf("s3store.NewClient: %v", err)
	}
	if err := client.EnsureBucket(ctx); err != nil {
		t.Fatalf("EnsureBucket: %v", err)
	}

	masterKey := make([]byte, crypto.KeySize)
	if _, err := io.ReadFull(rand.Reader, masterKey); err != nil {
		t.Fatal(err)
	}
	s3ConfigMgr, err := s3config.NewManager(env.s3ConfigRepo, masterKey)
	if err != nil {
		t.Fatalf("s3config.NewManager: %v", err)
	}
	if _, err := s3ConfigMgr.Save(ctx, s3config.SaveParams{
		Enabled: true, Endpoint: srv.Endpoint, Region: "us-east-1", Bucket: "marchi-retention-test",
		AccessKey: srv.AccessKeyID, SecretKey: srv.SecretKey, PathStyle: true,
	}); err != nil {
		t.Fatalf("saving s3 config: %v", err)
	}

	// Account: evict to S3-only after 7 days, delete from S3 entirely
	// after another 30 days (small numbers, this is a test, not the
	// product's real 2555-day default).
	accountID := env.createAccount(t, "lifecycle@example.com", nil)
	acct, err := env.accountsRepo.GetByID(ctx, accountID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	acct.RetentionMoveToS3Days = intPtr(7)
	acct.RetentionS3Days = intPtr(30)
	if err := env.accountsRepo.Update(ctx, acct); err != nil {
		t.Fatalf("Update account retention policy: %v", err)
	}

	fixedNow := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	archivedAt := fixedNow.AddDate(0, 0, -8) // 8 days ago: past the 7-day move-to-S3 threshold
	emailID, localPath := env.createEmail(t, accountID, 1, archivedAt)

	// Upload the real content to S3 and record a confirmed s3_etag/s3_key
	// — exactly what internal/s3store.Uploader would have done for real
	// by the time retention ever looks at this email (brief.md §4.6: the
	// upload must be confirmed before Stage A->B can evict the local copy).
	content, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatalf("reading seeded .eml: %v", err)
	}
	s3Key := s3store.EmailKey(accountID, archivedAt, s3store.ContentSHA256Hex(content))
	etag, err := client.Put(ctx, s3Key, bytes.NewReader(content), nil)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, err := env.sqlDB.Exec(`UPDATE emails SET s3_key = ?, s3_etag = ? WHERE id = ?`, s3Key, etag, emailID); err != nil {
		t.Fatalf("recording confirmed s3 upload: %v", err)
	}

	runner := New(Deps{
		AccountsRepo: env.accountsRepo, EmailsRepo: env.emailsRepo,
		RetentionSettingsRepo: env.retentionSettingsRepo, S3ConfigRepo: env.s3ConfigRepo,
		S3ConfigManager: s3ConfigMgr, Writer: env.w, Now: func() time.Time { return fixedNow },
	})

	// --- Stage A -> B ---
	stats, err := runner.Run(ctx)
	if err != nil {
		t.Fatalf("Run (Stage A->B): %v", err)
	}
	if stats.MovedToS3Only != 1 || stats.Errors != 0 {
		t.Fatalf("stats = %+v, want MovedToS3Only=1 Errors=0", stats)
	}

	afterA, err := env.emailsRepo.GetByID(ctx, emailID)
	if err != nil {
		t.Fatalf("GetByID after Stage A->B: %v", err)
	}
	if afterA.StorageLocation != "s3" || afterA.LocalPath != "" {
		t.Fatalf("after Stage A->B: got %+v, want storage_location=s3 and cleared LocalPath", afterA)
	}
	if _, err := os.Stat(localPath); !os.IsNotExist(err) {
		t.Error("local file still exists after Stage A->B, want deleted")
	}
	if _, _, err := client.Get(ctx, s3Key); err != nil {
		t.Errorf("S3 object missing after Stage A->B (should still exist, only the local copy is evicted): %v", err)
	}

	// --- Stage B -> C: advance the clock 31 days past Stage B's start ---
	fixedNow = fixedNow.AddDate(0, 0, 31)
	stats, err = runner.Run(ctx)
	if err != nil {
		t.Fatalf("Run (Stage B->C): %v", err)
	}
	if stats.DeletedFromS3 != 1 || stats.Errors != 0 {
		t.Fatalf("stats = %+v, want DeletedFromS3=1 Errors=0", stats)
	}

	if _, err := env.emailsRepo.GetByID(ctx, emailID); err == nil {
		t.Error("email row still exists after Stage B->C, want deleted")
	}
	if _, _, err := client.Get(ctx, s3Key); err == nil {
		t.Error("S3 object still exists after Stage B->C, want deleted")
	}
}

// TestRunner_EvictToS3Only_LocalFileRemovalFails_RowStillMarkedS3Only
// covers evictToS3Only's os.Remove failure branch (a warning, not a
// Stats error — MarkMovedToS3Only's SQLite write already committed by
// the time os.Remove runs, so the row IS the source of truth). Points
// LocalPath at a non-empty directory instead of a file, forcing a real
// ENOTEMPTY failure rather than one that would happen to also match
// os.IsNotExist.
func TestRunner_EvictToS3Only_LocalFileRemovalFails_RowStillMarkedS3Only(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	srv := minio.Start(t)
	client, err := s3store.NewClient(s3store.Options{
		Endpoint: srv.Endpoint, Region: "us-east-1", Bucket: "marchi-retention-removal-test",
		AccessKeyID: srv.AccessKeyID, SecretAccessKey: srv.SecretKey, PathStyle: true,
	})
	if err != nil {
		t.Fatalf("s3store.NewClient: %v", err)
	}
	if err := client.EnsureBucket(ctx); err != nil {
		t.Fatalf("EnsureBucket: %v", err)
	}

	masterKey := make([]byte, crypto.KeySize)
	if _, err := io.ReadFull(rand.Reader, masterKey); err != nil {
		t.Fatal(err)
	}
	s3ConfigMgr, err := s3config.NewManager(env.s3ConfigRepo, masterKey)
	if err != nil {
		t.Fatalf("s3config.NewManager: %v", err)
	}
	if _, err := s3ConfigMgr.Save(ctx, s3config.SaveParams{
		Enabled: true, Endpoint: srv.Endpoint, Region: "us-east-1", Bucket: "marchi-retention-removal-test",
		AccessKey: srv.AccessKeyID, SecretKey: srv.SecretKey, PathStyle: true,
	}); err != nil {
		t.Fatalf("saving s3 config: %v", err)
	}

	accountID := env.createAccount(t, "removal-fail@example.com", nil)
	acct, err := env.accountsRepo.GetByID(ctx, accountID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	acct.RetentionMoveToS3Days = intPtr(7)
	if err := env.accountsRepo.Update(ctx, acct); err != nil {
		t.Fatalf("Update account retention policy: %v", err)
	}

	fixedNow := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	archivedAt := fixedNow.AddDate(0, 0, -8)
	emailID, localPath := env.createEmail(t, accountID, 1, archivedAt)

	content, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatalf("reading seeded .eml: %v", err)
	}
	s3Key := s3store.EmailKey(accountID, archivedAt, s3store.ContentSHA256Hex(content))
	etag, err := client.Put(ctx, s3Key, bytes.NewReader(content), nil)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, err := env.sqlDB.Exec(`UPDATE emails SET s3_key = ?, s3_etag = ? WHERE id = ?`, s3Key, etag, emailID); err != nil {
		t.Fatalf("recording confirmed s3 upload: %v", err)
	}

	// Replace the seeded file with a non-empty directory at the same path.
	if err := os.Remove(localPath); err != nil {
		t.Fatalf("removing seeded file: %v", err)
	}
	if err := os.MkdirAll(localPath, 0o755); err != nil {
		t.Fatalf("creating directory at LocalPath: %v", err)
	}
	if err := os.WriteFile(localPath+"/blocks-removal", []byte("x"), 0o644); err != nil {
		t.Fatalf("populating directory: %v", err)
	}

	runner := New(Deps{
		AccountsRepo: env.accountsRepo, EmailsRepo: env.emailsRepo,
		RetentionSettingsRepo: env.retentionSettingsRepo, S3ConfigRepo: env.s3ConfigRepo,
		S3ConfigManager: s3ConfigMgr, Writer: env.w, Now: func() time.Time { return fixedNow },
	})

	stats, err := runner.Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.MovedToS3Only != 1 || stats.Errors != 0 {
		t.Fatalf("stats = %+v, want MovedToS3Only=1 Errors=0 (removal failure is a warning, not a Stats error)", stats)
	}

	after, err := env.emailsRepo.GetByID(ctx, emailID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if after.StorageLocation != "s3" {
		t.Errorf("StorageLocation = %q, want s3 despite the on-disk removal failure", after.StorageLocation)
	}
	if _, err := os.Stat(localPath); err != nil {
		t.Error("directory at LocalPath was removed unexpectedly, want it left behind (removal failed)")
	}
}
