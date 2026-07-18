package s3store

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"testing"
	"time"

	"github.com/yurydemin/marchi/internal/security/crypto"
)

func randMasterKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, crypto.KeySize)
	if _, err := io.ReadFull(rand.Reader, k); err != nil {
		t.Fatal(err)
	}
	return k
}

func TestEncryptDecryptObject_RoundTrip(t *testing.T) {
	masterKey := randMasterKey(t)
	plaintext := []byte("From: a@example.com\r\nSubject: test\r\n\r\nbody content")

	body, meta, err := EncryptObject(masterKey, plaintext)
	if err != nil {
		t.Fatalf("EncryptObject: %v", err)
	}
	if bytes.Contains(body, plaintext) {
		t.Fatal("encrypted body contains the plaintext verbatim")
	}
	for _, key := range []string{MetaIV, MetaTag, MetaSHA256} {
		if meta[key] == "" {
			t.Errorf("metadata[%q] is empty", key)
		}
	}
	wantSum := sha256.Sum256(plaintext)
	if meta[MetaSHA256] != hex.EncodeToString(wantSum[:]) {
		t.Errorf("metadata[%q] = %q, want sha256 of original plaintext", MetaSHA256, meta[MetaSHA256])
	}

	got, err := DecryptObject(masterKey, body, meta)
	if err != nil {
		t.Fatalf("DecryptObject: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Errorf("round trip mismatch: got %q, want %q", got, plaintext)
	}
}

func TestDecryptObject_WrongMasterKeyFails(t *testing.T) {
	body, meta, err := EncryptObject(randMasterKey(t), []byte("secret"))
	if err != nil {
		t.Fatalf("EncryptObject: %v", err)
	}
	if _, err := DecryptObject(randMasterKey(t), body, meta); err == nil {
		t.Error("DecryptObject with wrong master key succeeded, want an error")
	}
}

func TestDecryptObject_TamperedBodyFails(t *testing.T) {
	masterKey := randMasterKey(t)
	body, meta, err := EncryptObject(masterKey, []byte("secret content"))
	if err != nil {
		t.Fatalf("EncryptObject: %v", err)
	}
	tampered := bytes.Clone(body)
	tampered[0] ^= 0xFF
	if _, err := DecryptObject(masterKey, tampered, meta); err == nil {
		t.Error("DecryptObject with tampered body succeeded, want an error")
	}
}

func TestDecryptObject_TamperedSHA256MetadataFails(t *testing.T) {
	masterKey := randMasterKey(t)
	body, meta, err := EncryptObject(masterKey, []byte("secret content"))
	if err != nil {
		t.Fatalf("EncryptObject: %v", err)
	}
	meta[MetaSHA256] = hex.EncodeToString(make([]byte, sha256.Size))
	if _, err := DecryptObject(masterKey, body, meta); err == nil {
		t.Error("DecryptObject with a mismatched sha256 metadata succeeded, want an error")
	}
}

// TestClient_EncryptedRoundTrip_AgainstRealMinIO is Phase 3 step 6's demo
// criterion: encrypt -> upload to a real S3-compatible store -> download
// -> decrypt -> SHA-256 matches.
func TestClient_EncryptedRoundTrip_AgainstRealMinIO(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	masterKey := randMasterKey(t)

	plaintext := []byte("From: a@example.com\r\nSubject: encrypted round trip\r\n\r\nbody")
	key := EmailKey(1, time.Date(2026, time.July, 19, 0, 0, 0, 0, time.UTC), "cafebabe")

	body, meta, err := EncryptObject(masterKey, plaintext)
	if err != nil {
		t.Fatalf("EncryptObject: %v", err)
	}
	if _, err := c.Put(ctx, key, bytes.NewReader(body), meta); err != nil {
		t.Fatalf("Put: %v", err)
	}

	downloaded, gotMeta, err := c.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	downloadedBody, err := io.ReadAll(downloaded)
	downloaded.Close()
	if err != nil {
		t.Fatalf("reading downloaded body: %v", err)
	}

	got, err := DecryptObject(masterKey, downloadedBody, gotMeta)
	if err != nil {
		t.Fatalf("DecryptObject: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Errorf("decrypted content = %q, want %q", got, plaintext)
	}

	wantSum := sha256.Sum256(plaintext)
	gotSum := sha256.Sum256(got)
	if gotSum != wantSum {
		t.Error("SHA-256 of decrypted content does not match original")
	}
}
