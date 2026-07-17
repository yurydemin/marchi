// Package crypto provides the AES-256-GCM encryption and HKDF subkey
// derivation primitives used throughout MailVault: encrypting IMAP
// passwords/OAuth2 tokens/S3 credentials before they touch SQLite, and
// client-side encrypting .eml files before they're uploaded to S3.
//
// This package has no notion of the Master Key lifecycle (unlock state,
// Argon2id derivation from a password, salt storage) — that lives in
// internal/security/masterkey. This package only deals with raw 32-byte
// keys handed to it by the caller.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/hkdf"
)

// KeySize is the required key length for all functions in this package: 32
// bytes, i.e. AES-256.
const KeySize = 32

// ErrInvalidKeySize is returned when a key isn't exactly KeySize bytes.
var ErrInvalidKeySize = fmt.Errorf("crypto: key must be %d bytes (AES-256)", KeySize)

// errDecryptFailed is deliberately generic: distinguishing "wrong key" from
// "tampered ciphertext" from "wrong additional data" to a caller would leak
// information useful to an attacker probing encrypted credentials.
var errDecryptFailed = errors.New("crypto: decryption failed")

// Encrypt seals plaintext with AES-256-GCM under key. additionalData is
// authenticated but not encrypted (e.g. bind ciphertext to a record ID so it
// can't be swapped for another row's ciphertext) — pass nil if not needed.
// The returned blob is nonce || ciphertext || tag, ready to store as-is.
func Encrypt(key, plaintext, additionalData []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("crypto: generating nonce: %w", err)
	}
	return gcm.Seal(nonce, nonce, plaintext, additionalData), nil
}

// Decrypt reverses Encrypt. It returns errDecryptFailed (without further
// detail) if the ciphertext was tampered with, the key is wrong, or
// additionalData doesn't match what was passed to Encrypt.
func Decrypt(key, ciphertext, additionalData []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) < gcm.NonceSize() {
		return nil, errDecryptFailed
	}
	nonce, ct := ciphertext[:gcm.NonceSize()], ciphertext[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, ct, additionalData)
	if err != nil {
		return nil, errDecryptFailed
	}
	return plaintext, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != KeySize {
		return nil, ErrInvalidKeySize
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("crypto: creating AES cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("crypto: creating GCM: %w", err)
	}
	return gcm, nil
}

// DeriveSubkey derives a KeySize-byte subkey from masterKey via HKDF-SHA256,
// scoped by info — a purpose label such as "credential-encryption" or
// "s3-object-encryption" — so the single Master Key yields independent,
// non-interchangeable keys per use case (§3.4.5 / FR-ST-05 of the tech spec).
// Deterministic: the same (masterKey, info) pair always yields the same subkey.
func DeriveSubkey(masterKey []byte, info string) ([]byte, error) {
	if len(masterKey) != KeySize {
		return nil, ErrInvalidKeySize
	}
	h := hkdf.New(sha256.New, masterKey, nil, []byte(info))
	subkey := make([]byte, KeySize)
	if _, err := io.ReadFull(h, subkey); err != nil {
		return nil, fmt.Errorf("crypto: deriving subkey: %w", err)
	}
	return subkey, nil
}
