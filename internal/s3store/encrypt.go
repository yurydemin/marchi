package s3store

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"

	"github.com/yurydemin/marchi/internal/security/crypto"
)

// subkeyInfo scopes the HKDF derivation from the Master Key so S3 object
// encryption uses a key independent from credential encryption
// (accounts.imap_password_encrypted etc.) — see crypto.DeriveSubkey's doc
// comment and FR-ST-05.
const subkeyInfo = "s3-object-encryption"

// S3 object metadata keys (FR-S3-05/FR-S3-08). The SDK sends these as
// x-amz-meta-{key} headers.
const (
	MetaIV     = "mailvault-iv"
	MetaTag    = "mailvault-tag"
	MetaSHA256 = "mailvault-sha256"
)

const (
	gcmNonceSize = 12
	gcmTagSize   = 16
)

// EncryptObject encrypts plaintext for upload: AES-256-GCM under a Master
// Key-derived subkey (FR-S3-05), with the SHA-256 of the *original*
// plaintext computed before encryption (FR-S3-08). It returns the
// ciphertext body to upload as the S3 object and the metadata headers to
// attach alongside it (IV, GCM tag, and content SHA-256, all needed to
// decrypt and verify on download).
func EncryptObject(masterKey, plaintext []byte) (body []byte, metadata map[string]string, err error) {
	subkey, err := crypto.DeriveSubkey(masterKey, subkeyInfo)
	if err != nil {
		return nil, nil, fmt.Errorf("s3store: deriving subkey: %w", err)
	}

	sum := sha256.Sum256(plaintext)

	blob, err := crypto.Encrypt(subkey, plaintext, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("s3store: encrypting: %w", err)
	}
	// blob is nonce || ciphertext || tag (crypto.Encrypt's format). The S3
	// body is everything after the nonce; the nonce and the trailing GCM
	// tag are additionally surfaced as metadata per FR-S3-05, though
	// decryption only needs the nonce back — the tag travels with the
	// body and is verified as part of GCM's Open call.
	iv := blob[:gcmNonceSize]
	body = blob[gcmNonceSize:]
	tag := body[len(body)-gcmTagSize:]

	metadata = map[string]string{
		MetaIV:     base64.StdEncoding.EncodeToString(iv),
		MetaTag:    base64.StdEncoding.EncodeToString(tag),
		MetaSHA256: hex.EncodeToString(sum[:]),
	}
	return body, metadata, nil
}

// DecryptObject reverses EncryptObject: it reconstructs the full
// nonce||ciphertext||tag blob from body and the object's IV metadata,
// decrypts it, and verifies the decrypted plaintext's SHA-256 against the
// sha256 metadata (FR-S3-08) — catching silent corruption or a
// mismatched/stale metadata header that GCM's own tag check wouldn't.
func DecryptObject(masterKey, body []byte, metadata map[string]string) ([]byte, error) {
	subkey, err := crypto.DeriveSubkey(masterKey, subkeyInfo)
	if err != nil {
		return nil, fmt.Errorf("s3store: deriving subkey: %w", err)
	}

	ivB64, ok := metadata[MetaIV]
	if !ok {
		return nil, fmt.Errorf("s3store: object metadata missing %q", MetaIV)
	}
	iv, err := base64.StdEncoding.DecodeString(ivB64)
	if err != nil {
		return nil, fmt.Errorf("s3store: decoding %q metadata: %w", MetaIV, err)
	}

	blob := make([]byte, 0, len(iv)+len(body))
	blob = append(blob, iv...)
	blob = append(blob, body...)

	plaintext, err := crypto.Decrypt(subkey, blob, nil)
	if err != nil {
		return nil, fmt.Errorf("s3store: decrypting: %w", err)
	}

	wantSHA256, ok := metadata[MetaSHA256]
	if !ok {
		return nil, fmt.Errorf("s3store: object metadata missing %q", MetaSHA256)
	}
	sum := sha256.Sum256(plaintext)
	if hex.EncodeToString(sum[:]) != wantSHA256 {
		return nil, fmt.Errorf("s3store: sha256 mismatch after decryption: object metadata says %s", wantSHA256)
	}

	return plaintext, nil
}
