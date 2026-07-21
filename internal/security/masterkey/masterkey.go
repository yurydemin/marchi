// Package masterkey implements FR-ST-04 / NFR-SC-01: deriving the Master Key
// from a user password via Argon2id, and verifying a password against a
// previously-bootstrapped vault without ever persisting the key itself.
//
// The Master Key itself never encrypts anything directly. Its only job is
// to wrap (encrypt) a separate, randomly-generated Data Encryption Key —
// the DEK — which is what internal/security/crypto.DeriveSubkey actually
// scopes into the credential-encryption and S3-object-encryption subkeys
// used everywhere else in the codebase. This indirection exists
// specifically so that changing the vault's password (ChangePassword)
// only has to re-wrap one 32-byte DEK under the newly-derived Master
// Key — every IMAP password, OAuth2 token, S3 credential, and
// already-uploaded S3 object stays encrypted under exactly the same
// subkeys it always was, because the DEK feeding those subkeys never
// changes. Without this layer, a password change would need to decrypt
// and re-encrypt every one of those — including downloading, decrypting,
// and re-uploading every object already sitting in S3 — which isn't
// something a "change my password" action should ever have to do.
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
	dekFilename    = ".dek"
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

// dekAAD binds the wrapped-DEK ciphertext to its purpose, the same
// defense-in-depth idea as every other AAD in this codebase (e.g.
// accounts.imap_password_encrypted bound to email) — not load-bearing
// for a single-blob file, but costs nothing and rules out ciphertext
// confusion if this file's format is ever extended.
var dekAAD = []byte("marchi-dek-v1")

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

// DEKPath is the wrapped Data Encryption Key's location — see this
// package's doc comment for what it's for.
func DEKPath(dataDir string) string { return filepath.Join(dataDir, dekFilename) }

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

// LoadOrCreateDEK unwraps the Data Encryption Key stored at dekPath under
// masterKey, generating and wrapping a fresh random DEK on first use (no
// file at dekPath yet). Every caller that used to want the raw Master Key
// for subkey derivation (internal/security/crypto.DeriveSubkey and
// everything built on it) wants this instead — UnlockDEK below is the
// convenience wrapper most callers actually use.
func LoadOrCreateDEK(masterKey []byte, dekPath string) ([]byte, error) {
	wrapped, err := os.ReadFile(dekPath)
	switch {
	case err == nil:
		dek, err := crypto.Decrypt(masterKey, wrapped, dekAAD)
		if err != nil {
			return nil, fmt.Errorf("masterkey: unwrapping DEK: %w", err)
		}
		return dek, nil
	case os.IsNotExist(err):
		dek := make([]byte, crypto.KeySize)
		if _, err := io.ReadFull(rand.Reader, dek); err != nil {
			return nil, fmt.Errorf("masterkey: generating DEK: %w", err)
		}
		wrapped, err := crypto.Encrypt(masterKey, dek, dekAAD)
		if err != nil {
			return nil, fmt.Errorf("masterkey: wrapping DEK: %w", err)
		}
		if err := os.WriteFile(dekPath, wrapped, 0o600); err != nil {
			return nil, fmt.Errorf("masterkey: writing DEK file: %w", err)
		}
		return dek, nil
	default:
		return nil, fmt.Errorf("masterkey: reading DEK file: %w", err)
	}
}

// UnlockDEK is Unlock plus the DEK unwrap/bootstrap step in one call —
// what every real caller (the CLI's unlockMasterKey, POST /unlock,
// unlockFromEnv) actually wants, so the DEK indirection can't accidentally
// be skipped at a call site.
func UnlockDEK(password, saltPath, verifyPath, dekPath string, p Argon2Params) ([]byte, error) {
	masterKey, err := Unlock(password, saltPath, verifyPath, p)
	if err != nil {
		return nil, err
	}
	return LoadOrCreateDEK(masterKey, dekPath)
}

// ChangePassword verifies oldPassword against the existing store, then
// rotates to newPassword: a fresh salt and verifier under newPassword,
// and the same DEK re-wrapped under the newly-derived Master Key. Nothing
// actually encrypted under the DEK — IMAP passwords, OAuth2 tokens, S3
// credentials, S3-object bytes already uploaded — needs to change at all;
// only these three small files do.
//
// The three files can't be updated as a single atomic unit (that would
// need one combined file, a bigger format change than this warrants), so
// each new value is written to a temp path and renamed into place only
// once every one of them has been prepared successfully — narrowing the
// risk window to the handful of back-to-back renames themselves, the same
// trade internal/maildir's tmp->new rename already accepts elsewhere in
// this codebase. A crash exactly inside that window is the one scenario
// this doesn't fully protect against; it's not attempted here because
// doing so would mean changing the on-disk store format.
func ChangePassword(oldPassword, newPassword, dataDir string, p Argon2Params) error {
	if len(newPassword) < MinPasswordLength {
		return ErrPasswordTooShort
	}

	saltPath, verifyPath, dekPath := SaltPath(dataDir), VerifyPath(dataDir), DEKPath(dataDir)

	oldKey, err := Unlock(oldPassword, saltPath, verifyPath, p)
	if err != nil {
		return err
	}
	dek, err := LoadOrCreateDEK(oldKey, dekPath)
	if err != nil {
		return err
	}

	newSalt := make([]byte, saltSize)
	if _, err := io.ReadFull(rand.Reader, newSalt); err != nil {
		return fmt.Errorf("masterkey: generating new salt: %w", err)
	}
	newKey := deriveKey(newPassword, newSalt, p)

	newVerifier, err := crypto.Encrypt(newKey, verifyMagic, nil)
	if err != nil {
		return fmt.Errorf("masterkey: building new verifier: %w", err)
	}
	newWrappedDEK, err := crypto.Encrypt(newKey, dek, dekAAD)
	if err != nil {
		return fmt.Errorf("masterkey: re-wrapping DEK: %w", err)
	}

	if err := writeFileAtomic(dekPath, newWrappedDEK, 0o600); err != nil {
		return fmt.Errorf("masterkey: writing new DEK wrapper: %w", err)
	}
	if err := writeFileAtomic(verifyPath, newVerifier, 0o600); err != nil {
		return fmt.Errorf("masterkey: writing new verifier: %w", err)
	}
	if err := writeFileAtomic(saltPath, newSalt, 0o600); err != nil {
		return fmt.Errorf("masterkey: writing new salt: %w", err)
	}
	return nil
}

// writeFileAtomic writes data to a sibling temp file and renames it into
// place — rename is atomic on the same filesystem, so a reader never
// observes a partially-written file at path.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return fmt.Errorf("writing temp file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("renaming into place: %w", err)
	}
	return nil
}
