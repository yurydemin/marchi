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

func openTestS3ConfigRepo(t *testing.T) *S3ConfigRepo {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mailvault.db")
	sqlDB, err := db.Open(path)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })

	w := writer.New(sqlDB)
	t.Cleanup(func() { w.Close() })

	return NewS3ConfigRepo(sqlDB, w)
}

func TestS3ConfigRepo_Get_NeverConfigured(t *testing.T) {
	repo := openTestS3ConfigRepo(t)
	if _, err := repo.Get(context.Background()); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("Get before Upsert = %v, want sql.ErrNoRows", err)
	}
}

func TestS3ConfigRepo_UpsertAndGet_RoundTrips(t *testing.T) {
	repo := openTestS3ConfigRepo(t)
	ctx := context.Background()

	settings := &domain.S3Settings{
		Enabled: true, Endpoint: "https://s3.example.com", Region: "us-east-1", Bucket: "mailvault",
		AccessKeyEncrypted: []byte("encrypted-access-key"), SecretKeyEncrypted: []byte("encrypted-secret-key"),
		PathStyle: true, StorageClass: "STANDARD_IA", TLSSkipVerify: true,
	}
	if err := repo.Upsert(ctx, settings); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, err := repo.Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.Enabled || got.Endpoint != settings.Endpoint || got.Region != settings.Region || got.Bucket != settings.Bucket {
		t.Errorf("got = %+v, want matching Enabled/Endpoint/Region/Bucket from %+v", got, settings)
	}
	if string(got.AccessKeyEncrypted) != string(settings.AccessKeyEncrypted) {
		t.Errorf("AccessKeyEncrypted = %q, want %q", got.AccessKeyEncrypted, settings.AccessKeyEncrypted)
	}
	if string(got.SecretKeyEncrypted) != string(settings.SecretKeyEncrypted) {
		t.Errorf("SecretKeyEncrypted = %q, want %q", got.SecretKeyEncrypted, settings.SecretKeyEncrypted)
	}
	if !got.PathStyle || got.StorageClass != "STANDARD_IA" || !got.TLSSkipVerify {
		t.Errorf("got = %+v, want PathStyle/StorageClass/TLSSkipVerify matching", got)
	}
	if got.UpdatedAt.IsZero() {
		t.Error("UpdatedAt is zero, want a timestamp")
	}
}

func TestS3ConfigRepo_Upsert_OverwritesSingletonRow(t *testing.T) {
	repo := openTestS3ConfigRepo(t)
	ctx := context.Background()

	if err := repo.Upsert(ctx, &domain.S3Settings{Enabled: true, Endpoint: "https://first.example.com", Bucket: "a"}); err != nil {
		t.Fatalf("first Upsert: %v", err)
	}
	if err := repo.Upsert(ctx, &domain.S3Settings{Enabled: false, Endpoint: "https://second.example.com", Bucket: "b"}); err != nil {
		t.Fatalf("second Upsert: %v", err)
	}

	got, err := repo.Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Enabled || got.Endpoint != "https://second.example.com" || got.Bucket != "b" {
		t.Errorf("got = %+v, want the second Upsert's values (no duplicate row)", got)
	}
}
