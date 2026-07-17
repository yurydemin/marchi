package crypto

import (
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"testing"
)

func randKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, KeySize)
	if _, err := io.ReadFull(rand.Reader, k); err != nil {
		t.Fatal(err)
	}
	return k
}

func TestEncryptDecrypt_RoundTrip(t *testing.T) {
	key := randKey(t)
	plaintext := []byte("s3kr3t IMAP password")

	ct, err := Encrypt(key, plaintext, nil)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if bytes.Contains(ct, plaintext) {
		t.Fatal("ciphertext contains the plaintext verbatim")
	}

	pt, err := Decrypt(key, ct, nil)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if !bytes.Equal(pt, plaintext) {
		t.Errorf("round trip mismatch: got %q, want %q", pt, plaintext)
	}
}

func TestEncryptDecrypt_WithAdditionalData(t *testing.T) {
	key := randKey(t)
	plaintext := []byte("attachment bytes")
	aad := []byte("email_id=42")

	ct, err := Encrypt(key, plaintext, aad)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if _, err := Decrypt(key, ct, aad); err != nil {
		t.Fatalf("Decrypt with matching AAD should succeed: %v", err)
	}
	if _, err := Decrypt(key, ct, []byte("email_id=43")); err == nil {
		t.Error("Decrypt with mismatched AAD should fail, got nil error")
	}
}

func TestDecrypt_TamperedCiphertextFails(t *testing.T) {
	key := randKey(t)
	ct, err := Encrypt(key, []byte("original content"), nil)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	tampered := bytes.Clone(ct)
	tampered[len(tampered)-1] ^= 0xFF // flip a bit in the GCM tag
	if _, err := Decrypt(key, tampered, nil); err == nil {
		t.Error("expected tamper detection to fail decryption, got nil error")
	}
}

func TestDecrypt_WrongKeyFails(t *testing.T) {
	key1, key2 := randKey(t), randKey(t)
	ct, err := Encrypt(key1, []byte("payload"), nil)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if _, err := Decrypt(key2, ct, nil); err == nil {
		t.Error("expected decryption with wrong key to fail, got nil error")
	}
}

func TestEncrypt_NoncesAreUnique(t *testing.T) {
	key := randKey(t)
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		ct, err := Encrypt(key, []byte("same plaintext every time"), nil)
		if err != nil {
			t.Fatalf("Encrypt: %v", err)
		}
		nonce := string(ct[:12]) // AES-GCM standard nonce size
		if seen[nonce] {
			t.Fatal("nonce reused across encryptions — catastrophic for GCM")
		}
		seen[nonce] = true
	}
}

func TestEncryptDecrypt_InvalidKeySize(t *testing.T) {
	shortKey := []byte("too-short")
	if _, err := Encrypt(shortKey, []byte("x"), nil); !errors.Is(err, ErrInvalidKeySize) {
		t.Errorf("Encrypt with short key: got %v, want ErrInvalidKeySize", err)
	}
	if _, err := Decrypt(shortKey, []byte("x"), nil); !errors.Is(err, ErrInvalidKeySize) {
		t.Errorf("Decrypt with short key: got %v, want ErrInvalidKeySize", err)
	}
}

func TestDecrypt_TruncatedCiphertext(t *testing.T) {
	key := randKey(t)
	if _, err := Decrypt(key, []byte("short"), nil); err == nil {
		t.Error("expected error for ciphertext shorter than a nonce, got nil")
	}
}

func TestDeriveSubkey_Deterministic(t *testing.T) {
	masterKey := randKey(t)
	k1, err := DeriveSubkey(masterKey, "s3-object-encryption")
	if err != nil {
		t.Fatalf("DeriveSubkey: %v", err)
	}
	k2, err := DeriveSubkey(masterKey, "s3-object-encryption")
	if err != nil {
		t.Fatalf("DeriveSubkey: %v", err)
	}
	if !bytes.Equal(k1, k2) {
		t.Error("DeriveSubkey should be deterministic for the same (masterKey, info)")
	}
}

func TestDeriveSubkey_DifferentInfoYieldsDifferentKeys(t *testing.T) {
	masterKey := randKey(t)
	k1, err := DeriveSubkey(masterKey, "credential-encryption")
	if err != nil {
		t.Fatalf("DeriveSubkey: %v", err)
	}
	k2, err := DeriveSubkey(masterKey, "s3-object-encryption")
	if err != nil {
		t.Fatalf("DeriveSubkey: %v", err)
	}
	if bytes.Equal(k1, k2) {
		t.Error("different info labels must yield different subkeys")
	}
}

func TestDeriveSubkey_DifferentMasterKeysYieldDifferentSubkeys(t *testing.T) {
	k1, err := DeriveSubkey(randKey(t), "same-info")
	if err != nil {
		t.Fatalf("DeriveSubkey: %v", err)
	}
	k2, err := DeriveSubkey(randKey(t), "same-info")
	if err != nil {
		t.Fatalf("DeriveSubkey: %v", err)
	}
	if bytes.Equal(k1, k2) {
		t.Error("different master keys must yield different subkeys (collision astronomically unlikely)")
	}
}

func TestDeriveSubkey_UsableForEncryption(t *testing.T) {
	masterKey := randKey(t)
	subkey, err := DeriveSubkey(masterKey, "credential-encryption")
	if err != nil {
		t.Fatalf("DeriveSubkey: %v", err)
	}
	ct, err := Encrypt(subkey, []byte("derived-key round trip"), nil)
	if err != nil {
		t.Fatalf("Encrypt with derived subkey: %v", err)
	}
	pt, err := Decrypt(subkey, ct, nil)
	if err != nil {
		t.Fatalf("Decrypt with derived subkey: %v", err)
	}
	if string(pt) != "derived-key round trip" {
		t.Errorf("got %q", pt)
	}
}

func TestDeriveSubkey_InvalidMasterKeySize(t *testing.T) {
	if _, err := DeriveSubkey([]byte("short"), "info"); !errors.Is(err, ErrInvalidKeySize) {
		t.Errorf("got %v, want ErrInvalidKeySize", err)
	}
}
