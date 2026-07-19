// Package s3config is the S3 Settings Manager (FR-S3-02, Phase 3 step 9):
// the business-logic layer between the HTTP API and repo.S3ConfigRepo,
// mirroring internal/account's role for IMAP credentials. It's the only
// place that knows how to turn a plaintext access/secret key pair into
// what actually gets stored — repo.S3ConfigRepo just persists whatever
// bytes it's given.
package s3config

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/yurydemin/marchi/internal/account"
	"github.com/yurydemin/marchi/internal/db/repo"
	"github.com/yurydemin/marchi/internal/domain"
	"github.com/yurydemin/marchi/internal/security/crypto"
)

// s3ConfigAAD binds the encrypted access/secret keys to this specific use
// (as opposed to, say, an IMAP password ciphertext being fed in by
// mistake) — s3_config is a singleton row, so there's no per-record id to
// bind against the way accounts.imap_password_encrypted binds to email.
const s3ConfigAAD = "s3-config"

// Manager wraps S3ConfigRepo with the credential-encryption subkey
// (account.CredentialSubkey — the same one IMAP passwords use, per
// FR-ST-05's "one Master Key, per-purpose subkeys" model applied at the
// use-case level: "credential", not "S3 credential specifically").
type Manager struct {
	repo *repo.S3ConfigRepo
	key  []byte
}

// NewManager derives the credential-encryption subkey from masterKey.
// Call this once per unlock.
func NewManager(s3ConfigRepo *repo.S3ConfigRepo, masterKey []byte) (*Manager, error) {
	key, err := account.CredentialSubkey(masterKey)
	if err != nil {
		return nil, err
	}
	return &Manager{repo: s3ConfigRepo, key: key}, nil
}

// SaveParams is the plaintext input for creating or updating the S3
// settings singleton. AccessKey/SecretKey are optional on an update:
// empty keeps whatever is already stored, the same convention
// account.UpdateAccountParams.IMAPPassword uses, so a settings form can
// resubmit everything else without forcing the user to re-paste secrets
// they aren't changing.
type SaveParams struct {
	Enabled       bool
	Endpoint      string
	Region        string
	Bucket        string
	AccessKey     string // "" on update keeps the existing encrypted value
	SecretKey     string // "" on update keeps the existing encrypted value
	PathStyle     bool
	StorageClass  string
	TLSSkipVerify bool
}

// Save validates params, encrypts AccessKey/SecretKey (if provided), and
// upserts the singleton row. Returns the persisted settings.
func (m *Manager) Save(ctx context.Context, p SaveParams) (*domain.S3Settings, error) {
	if err := validateSaveParams(p); err != nil {
		return nil, err
	}

	existing, err := m.repo.Get(ctx)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("s3config: loading existing settings: %w", err)
	}

	s := &domain.S3Settings{
		Enabled: p.Enabled, Endpoint: p.Endpoint, Region: p.Region, Bucket: p.Bucket,
		PathStyle: p.PathStyle, StorageClass: p.StorageClass, TLSSkipVerify: p.TLSSkipVerify,
	}
	if s.StorageClass == "" {
		s.StorageClass = "STANDARD"
	}

	if p.AccessKey != "" {
		enc, err := crypto.Encrypt(m.key, []byte(p.AccessKey), []byte(s3ConfigAAD))
		if err != nil {
			return nil, fmt.Errorf("s3config: encrypting access key: %w", err)
		}
		s.AccessKeyEncrypted = enc
	} else if existing != nil {
		s.AccessKeyEncrypted = existing.AccessKeyEncrypted
	}

	if p.SecretKey != "" {
		enc, err := crypto.Encrypt(m.key, []byte(p.SecretKey), []byte(s3ConfigAAD))
		if err != nil {
			return nil, fmt.Errorf("s3config: encrypting secret key: %w", err)
		}
		s.SecretKeyEncrypted = enc
	} else if existing != nil {
		s.SecretKeyEncrypted = existing.SecretKeyEncrypted
	}

	if err := m.repo.Upsert(ctx, s); err != nil {
		return nil, err
	}
	return m.repo.Get(ctx)
}

// Get returns the currently saved settings, or repo's sql.ErrNoRows if S3
// has never been configured.
func (m *Manager) Get(ctx context.Context) (*domain.S3Settings, error) {
	return m.repo.Get(ctx)
}

// DecryptCredentials returns s's plaintext access/secret key pair — used
// right before building an s3store.Client (test-connection, the Uploader
// worker pool), never stored or logged beyond that.
func (m *Manager) DecryptCredentials(s *domain.S3Settings) (accessKey, secretKey string, err error) {
	access, err := crypto.Decrypt(m.key, s.AccessKeyEncrypted, []byte(s3ConfigAAD))
	if err != nil {
		return "", "", fmt.Errorf("s3config: decrypting access key: %w", err)
	}
	secret, err := crypto.Decrypt(m.key, s.SecretKeyEncrypted, []byte(s3ConfigAAD))
	if err != nil {
		return "", "", fmt.Errorf("s3config: decrypting secret key: %w", err)
	}
	return string(access), string(secret), nil
}

func validateSaveParams(p SaveParams) error {
	if strings.TrimSpace(p.Bucket) == "" {
		return fmt.Errorf("s3config: bucket is required")
	}
	if strings.TrimSpace(p.Region) == "" {
		return fmt.Errorf("s3config: region is required")
	}
	return nil
}
