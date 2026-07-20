// Package masterkey implements FR-ST-04 / NFR-SC-01: deriving the Master Key
// from a user password via Argon2id, and verifying a password against a
// previously-bootstrapped vault without ever persisting the key itself.
//
// The Master Key is the raw material internal/security/crypto.DeriveSubkey
// then scopes into independent credential-encryption and S3-encryption
// subkeys — this package doesn't know about those use cases, only about
// turning a password into 32 bytes and checking that a candidate password
// is the right one.
package masterkey

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/crypto/argon2"

	"github.com/yurydemin/marchi/internal/security/crypto"
)

const (
	// MinPasswordLength matches FR-ST-04's "minimum 12 characters".
	MinPasswordLength = 12

	saltSize       = 16
	saltFilename   = ".salt"
	verifyFilename = ".mk-verify"
)

var (
	ErrPasswordTooShort  = fmt.Errorf("masterkey: password must be at least %d characters", MinPasswordLength)
	ErrPasswordMismatch  = errors.New("masterkey: password confirmation does not match")
	ErrIncorrectPassword = errors.New("masterkey: incorrect password")
	// ErrCorruptedStore means the salt file exists but the verifier doesn't
	// — bootstrapping a fresh salt here would silently orphan anything
	// already encrypted under the original key, so this is a hard stop
	// rather than a fallback to "first run".
	ErrCorruptedStore = errors.New("masterkey: salt file exists but the verifier file is missing or unreadable; key store may be corrupted")
)

// verifyMagic is the fixed plaintext encrypted under the derived key to
// build the verifier blob; a password is "correct" iff decrypting the
// stored verifier with the freshly-derived key yields this back.
var verifyMagic = []byte("marchi-master-key-v1")

// Argon2Params mirrors config.Argon2Config, kept as this package's own type
// so it doesn't depend on internal/config.
type Argon2Params struct {
	Memory      uint32
	Iterations  uint32
	Parallelism uint8
}

// SaltPath and VerifyPath are the fixed, spec-mandated locations under
// data_dir (FR-ST-04: "{data_dir}/.salt").
func SaltPath(dataDir string) string   { return filepath.Join(dataDir, saltFilename) }
func VerifyPath(dataDir string) string { return filepath.Join(dataDir, verifyFilename) }

func deriveKey(password string, salt []byte, p Argon2Params) []byte {
	return argon2.IDKey([]byte(password), salt, p.Iterations, p.Memory, p.Parallelism, uint32(crypto.KeySize))
}

// IsFirstRun reports whether no salt file exists yet at saltPath — the
// caller uses this to decide whether to prompt for a NEW password (with
// confirmation) or an EXISTING one.
func IsFirstRun(saltPath string) bool {
	_, err := os.Stat(saltPath)
	return os.IsNotExist(err)
}

// Unlock derives the Master Key from password. On first run (no salt file
// at saltPath) it bootstraps a random salt and a verifier blob from this
// password and returns the derived key. On subsequent runs it verifies
// password against the stored verifier, returning ErrIncorrectPassword on
// mismatch. The derived key is never written to disk in either case.
func Unlock(password, saltPath, verifyPath string, p Argon2Params) ([]byte, error) {
	if len(password) < MinPasswordLength {
		return nil, ErrPasswordTooShort
	}

	salt, err := os.ReadFile(saltPath)
	switch {
	case err == nil:
		return unlockExisting(password, salt, verifyPath, p)
	case os.IsNotExist(err):
		return bootstrap(password, saltPath, verifyPath, p)
	default:
		return nil, fmt.Errorf("masterkey: reading salt file: %w", err)
	}
}

func bootstrap(password, saltPath, verifyPath string, p Argon2Params) ([]byte, error) {
	salt := make([]byte, saltSize)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, fmt.Errorf("masterkey: generating salt: %w", err)
	}
	key := deriveKey(password, salt, p)

	verifier, err := crypto.Encrypt(key, verifyMagic, nil)
	if err != nil {
		return nil, fmt.Errorf("masterkey: building verifier: %w", err)
	}

	if err := os.WriteFile(saltPath, salt, 0o600); err != nil {
		return nil, fmt.Errorf("masterkey: writing salt file: %w", err)
	}
	if err := os.WriteFile(verifyPath, verifier, 0o600); err != nil {
		return nil, fmt.Errorf("masterkey: writing verifier file: %w", err)
	}
	return key, nil
}

func unlockExisting(password string, salt []byte, verifyPath string, p Argon2Params) ([]byte, error) {
	verifier, err := os.ReadFile(verifyPath)
	if os.IsNotExist(err) {
		return nil, ErrCorruptedStore
	}
	if err != nil {
		return nil, fmt.Errorf("masterkey: reading verifier file: %w", err)
	}

	key := deriveKey(password, salt, p)
	plaintext, err := crypto.Decrypt(key, verifier, nil)
	if err != nil || !bytes.Equal(plaintext, verifyMagic) {
		return nil, ErrIncorrectPassword
	}
	return key, nil
}
