// Package account is the Account Manager (FR-AM-01..06): the business-logic
// layer between the CLI/HTTP handlers and repo.AccountsRepo. It's the only
// place that knows how to turn a plaintext IMAP password into what actually
// gets stored — repo.AccountsRepo just persists whatever bytes it's given.
package account

import (
	"context"
	"fmt"
	"strings"

	"github.com/yurydemin/marchi/internal/db/repo"
	"github.com/yurydemin/marchi/internal/domain"
	"github.com/yurydemin/marchi/internal/security/crypto"
)

// credentialSubkeyInfo scopes the HKDF subkey used for encrypting IMAP
// passwords / OAuth2 tokens / S3 access & secret keys, independent of the
// S3 client-side-object-encryption subkey internal/s3store derives under
// its own label (FR-ST-05: one Master Key, non-interchangeable derived
// keys per use case). Every kind of "credential" this app stores shares
// this one subkey — see CredentialSubkey.
const credentialSubkeyInfo = "credential-encryption"

// CredentialSubkey derives the shared credential-encryption subkey from
// masterKey — exported so internal/s3config can encrypt/decrypt S3 access
// and secret keys under the exact same subkey Manager uses for IMAP
// passwords, without duplicating the "credential-encryption" label in a
// second place that could drift out of sync.
func CredentialSubkey(masterKey []byte) ([]byte, error) {
	key, err := crypto.DeriveSubkey(masterKey, credentialSubkeyInfo)
	if err != nil {
		return nil, fmt.Errorf("account: deriving credential subkey: %w", err)
	}
	return key, nil
}

// Manager wraps AccountsRepo with the credential-encryption subkey derived
// once from the unlocked Master Key.
type Manager struct {
	repo *repo.AccountsRepo
	key  []byte
}

// NewManager derives the credential-encryption subkey from masterKey. Call
// this once per unlock, after internal/security/masterkey has produced the
// Master Key.
func NewManager(accountsRepo *repo.AccountsRepo, masterKey []byte) (*Manager, error) {
	key, err := CredentialSubkey(masterKey)
	if err != nil {
		return nil, err
	}
	return &Manager{repo: accountsRepo, key: key}, nil
}

// AddAccountParams is the plaintext input for adding a plain-password IMAP
// account (FR-AM-01). OAuth2 accounts are out of scope until Phase 3.
type AddAccountParams struct {
	Email        string
	DisplayName  string
	IMAPHost     string
	IMAPPort     int // 0 picks a sensible default for IMAPTLS
	IMAPTLS      domain.IMAPTLSMode
	IMAPUsername string // defaults to Email if empty
	IMAPPassword string // plaintext; encrypted before it ever reaches the repo
}

// AddAccount validates params, encrypts the password, and persists the
// account. The returned Account carries the encrypted blob, never the
// plaintext password.
func (m *Manager) AddAccount(ctx context.Context, p AddAccountParams) (*domain.Account, error) {
	if err := validateAddAccountParams(p); err != nil {
		return nil, err
	}

	username := p.IMAPUsername
	if username == "" {
		username = p.Email
	}
	port := p.IMAPPort
	if port == 0 {
		port = defaultPortFor(p.IMAPTLS)
	}

	// AAD binds the ciphertext to this account's email, so swapping one
	// account's encrypted blob into another row fails decryption loudly
	// instead of silently producing garbage under the wrong context.
	encPassword, err := crypto.Encrypt(m.key, []byte(p.IMAPPassword), []byte(p.Email))
	if err != nil {
		return nil, fmt.Errorf("account: encrypting password: %w", err)
	}

	a := &domain.Account{
		Email:                 p.Email,
		DisplayName:           p.DisplayName,
		IMAPHost:              p.IMAPHost,
		IMAPPort:              port,
		IMAPTLS:               p.IMAPTLS,
		IMAPUsername:          username,
		IMAPPasswordEncrypted: encPassword,
		IsActive:              true,
	}

	id, err := m.repo.Create(ctx, a)
	if err != nil {
		return nil, err
	}
	// Re-fetch rather than just setting a.ID: CreatedAt/UpdatedAt are
	// DB-generated defaults Create never reads back, so the in-memory a
	// would otherwise report the zero time instead of what's actually
	// stored — visible now that the REST API echoes this struct as JSON.
	return m.repo.GetByID(ctx, id)
}

// UpdateAccountParams is the plaintext input for editing an existing
// account (FR-AM-04's "редактирование", FR-AM-05's is_active toggle).
// IMAPPassword is optional: empty means "keep the currently stored
// password" rather than clearing it, since an edit form generally
// shouldn't have to resubmit a password the user isn't changing.
type UpdateAccountParams struct {
	DisplayName  string
	IMAPHost     string
	IMAPPort     int
	IMAPTLS      domain.IMAPTLSMode
	IMAPUsername string
	IMAPPassword string // "" keeps the existing encrypted password
	IsActive     *bool  // nil keeps the existing value — omitting it on an edit shouldn't silently pause sync (FR-AM-05)
	SyncCron     string
}

// UpdateAccount fetches the account by id, applies p on top of it, and
// persists the result. Returns sql.ErrNoRows if id doesn't exist.
func (m *Manager) UpdateAccount(ctx context.Context, id int64, p UpdateAccountParams) (*domain.Account, error) {
	a, err := m.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	if strings.TrimSpace(p.IMAPHost) == "" {
		return nil, fmt.Errorf("account: imap host is required")
	}
	if p.IMAPPort < 0 || p.IMAPPort > 65535 {
		return nil, fmt.Errorf("account: imap port must be between 0 and 65535, got %d", p.IMAPPort)
	}

	a.DisplayName = p.DisplayName
	a.IMAPHost = p.IMAPHost
	a.IMAPPort = p.IMAPPort
	if a.IMAPPort == 0 {
		a.IMAPPort = defaultPortFor(p.IMAPTLS)
	}
	a.IMAPTLS = p.IMAPTLS
	a.IMAPUsername = p.IMAPUsername
	if a.IMAPUsername == "" {
		a.IMAPUsername = a.Email
	}
	if p.IsActive != nil {
		a.IsActive = *p.IsActive
	}
	a.SyncCron = p.SyncCron

	if p.IMAPPassword != "" {
		encPassword, err := crypto.Encrypt(m.key, []byte(p.IMAPPassword), []byte(a.Email))
		if err != nil {
			return nil, fmt.Errorf("account: encrypting password: %w", err)
		}
		a.IMAPPasswordEncrypted = encPassword
	}

	if err := m.repo.Update(ctx, a); err != nil {
		return nil, err
	}
	// Same reasoning as AddAccount: a's UpdatedAt is from before this
	// update (it was fetched via GetByID at the top of this function), so
	// re-fetch to return what Update's CURRENT_TIMESTAMP actually wrote.
	return m.repo.GetByID(ctx, id)
}

// ListAccounts returns every configured account (metadata only — this
// doesn't touch the encrypted credential fields, so it works without the
// Master Key being involved at all beyond what NewManager already needed).
func (m *Manager) ListAccounts(ctx context.Context) ([]*domain.Account, error) {
	return m.repo.List(ctx)
}

// DecryptPassword returns a's plaintext IMAP password, right before it's
// needed to connect. Never stored, never logged — the caller should let it
// go out of scope as soon as the connection is established.
func (m *Manager) DecryptPassword(a *domain.Account) (string, error) {
	plain, err := crypto.Decrypt(m.key, a.IMAPPasswordEncrypted, []byte(a.Email))
	if err != nil {
		return "", fmt.Errorf("account: decrypting password: %w", err)
	}
	return string(plain), nil
}

func validateAddAccountParams(p AddAccountParams) error {
	if strings.TrimSpace(p.Email) == "" {
		return fmt.Errorf("account: email is required")
	}
	if strings.TrimSpace(p.IMAPHost) == "" {
		return fmt.Errorf("account: imap host is required")
	}
	if p.IMAPPort < 0 || p.IMAPPort > 65535 {
		return fmt.Errorf("account: imap port must be between 0 and 65535, got %d", p.IMAPPort)
	}
	if p.IMAPPassword == "" {
		return fmt.Errorf("account: imap password is required")
	}
	return nil
}

func defaultPortFor(tls domain.IMAPTLSMode) int {
	if tls == domain.IMAPTLSSSL {
		return 993
	}
	return 143
}
