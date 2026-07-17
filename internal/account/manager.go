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
// passwords / OAuth2 tokens, independent of the S3 client-side-encryption
// subkey (FR-ST-05: one Master Key, non-interchangeable derived keys).
const credentialSubkeyInfo = "credential-encryption"

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
	key, err := crypto.DeriveSubkey(masterKey, credentialSubkeyInfo)
	if err != nil {
		return nil, fmt.Errorf("account: deriving credential subkey: %w", err)
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
	a.ID = id
	return a, nil
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
