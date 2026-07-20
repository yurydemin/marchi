package s3config

import (
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"path/filepath"
	"testing"

	"github.com/yurydemin/marchi/internal/db"
	"github.com/yurydemin/marchi/internal/db/repo"
	"github.com/yurydemin/marchi/internal/db/writer"
	"github.com/yurydemin/marchi/internal/security/crypto"
)

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "marchi.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	w := writer.New(sqlDB)
	t.Cleanup(func() { w.Close() })

	masterKey := make([]byte, crypto.KeySize)
	if _, err := io.ReadFull(rand.Reader, masterKey); err != nil {
		t.Fatal(err)
	}

	mgr, err := NewManager(repo.NewS3ConfigRepo(sqlDB, w), masterKey)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return mgr
}

func TestManager_SaveAndGet_RoundTrips(t *testing.T) {
	mgr := newTestManager(t)
	ctx := context.Background()

	saved, err := mgr.Save(ctx, SaveParams{
		Enabled: true, Endpoint: "https://s3.example.com", Region: "us-east-1", Bucket: "marchi",
		AccessKey: "AKIAEXAMPLE", SecretKey: "supersecret", PathStyle: true, StorageClass: "STANDARD_IA",
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !saved.Enabled || saved.Bucket != "marchi" || saved.StorageClass != "STANDARD_IA" {
		t.Errorf("saved = %+v, unexpected fields", saved)
	}
	if bytes.Contains(saved.AccessKeyEncrypted, []byte("AKIAEXAMPLE")) {
		t.Error("AccessKeyEncrypted contains the plaintext access key verbatim")
	}
	if bytes.Contains(saved.SecretKeyEncrypted, []byte("supersecret")) {
		t.Error("SecretKeyEncrypted contains the plaintext secret key verbatim")
	}

	got, err := mgr.Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	access, secret, err := mgr.DecryptCredentials(got)
	if err != nil {
		t.Fatalf("DecryptCredentials: %v", err)
	}
	if access != "AKIAEXAMPLE" || secret != "supersecret" {
		t.Errorf("decrypted credentials = (%q, %q), want (AKIAEXAMPLE, supersecret)", access, secret)
	}
}

func TestManager_Save_EmptyKeysOnUpdate_KeepsExisting(t *testing.T) {
	mgr := newTestManager(t)
	ctx := context.Background()

	if _, err := mgr.Save(ctx, SaveParams{
		Bucket: "marchi", Region: "us-east-1", AccessKey: "AKIAEXAMPLE", SecretKey: "supersecret",
	}); err != nil {
		t.Fatalf("first Save: %v", err)
	}

	// Second save changes only Enabled/Endpoint, leaving AccessKey/SecretKey
	// blank — must keep the previously stored credentials, not wipe them.
	updated, err := mgr.Save(ctx, SaveParams{
		Enabled: true, Bucket: "marchi", Region: "us-east-1", Endpoint: "https://new.example.com",
	})
	if err != nil {
		t.Fatalf("second Save: %v", err)
	}
	access, secret, err := mgr.DecryptCredentials(updated)
	if err != nil {
		t.Fatalf("DecryptCredentials after keep-existing update: %v", err)
	}
	if access != "AKIAEXAMPLE" || secret != "supersecret" {
		t.Errorf("credentials after keep-existing update = (%q, %q), want unchanged (AKIAEXAMPLE, supersecret)", access, secret)
	}
	if !updated.Enabled || updated.Endpoint != "https://new.example.com" {
		t.Errorf("updated = %+v, want Enabled=true and the new endpoint", updated)
	}
}

func TestManager_Save_MissingBucket_Rejected(t *testing.T) {
	mgr := newTestManager(t)
	if _, err := mgr.Save(context.Background(), SaveParams{Region: "us-east-1"}); err == nil {
		t.Error("Save with no bucket succeeded, want a validation error")
	}
}

func TestManager_Save_MissingRegion_Rejected(t *testing.T) {
	mgr := newTestManager(t)
	if _, err := mgr.Save(context.Background(), SaveParams{Bucket: "marchi"}); err == nil {
		t.Error("Save with no region succeeded, want a validation error")
	}
}
