package account

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/yurydemin/marchi/internal/domain"
	oauth2pkg "github.com/yurydemin/marchi/internal/oauth2"
	"github.com/yurydemin/marchi/internal/security/crypto"
)

// gmailAPIPlaceholderIMAPHost fills accounts.imap_host (NOT NULL) for a
// ConnectorGmailAPI account, which never connects via IMAP at all — same
// reasoning as cmd/marchi/cmd_import.go's importPlaceholderIMAPHost: a
// value that fails DNS resolution loudly is safer here than one that
// might coincidentally resolve, in case anything ever tried to dial it
// by mistake.
const gmailAPIPlaceholderIMAPHost = "gmail-api.invalid"

// msgraphPlaceholderIMAPHost is gmailAPIPlaceholderIMAPHost's counterpart
// for a ConnectorMSGraph account — same reasoning, different connector.
const msgraphPlaceholderIMAPHost = "msgraph-api.invalid"

// AddOAuth2AccountParams is the plaintext input for adding an OAuth2
// account (FR-AM-01's Google/Microsoft path) — there's no IMAP password
// for these at all; the OAuth2 token itself is what authenticates.
// IMAPHost/IMAPPort/IMAPUsername are ignored when ConnectorType is
// ConnectorGmailAPI or ConnectorMSGraph (neither ever speaks IMAP — see
// gmailAPIPlaceholderIMAPHost/msgraphPlaceholderIMAPHost).
type AddOAuth2AccountParams struct {
	Email         string
	DisplayName   string
	IMAPHost      string
	IMAPPort      int // 0 picks a sensible default for IMAPTLS
	IMAPTLS       domain.IMAPTLSMode
	IMAPUsername  string               // defaults to Email if empty
	Provider      string               // domain.OAuth2ProviderGoogle or domain.OAuth2ProviderMicrosoft
	ConnectorType domain.ConnectorType // "" behaves as domain.ConnectorIMAP
	Token         oauth2pkg.Token
}

// AddOAuth2Account validates params, encrypts the token, and persists the
// account. Mirrors AddAccount's shape but for the OAuth2 path.
func (m *Manager) AddOAuth2Account(ctx context.Context, p AddOAuth2AccountParams) (*domain.Account, error) {
	if err := validateAddOAuth2AccountParams(p); err != nil {
		return nil, err
	}

	encToken, err := m.encryptOAuth2Token(p.Email, p.Token)
	if err != nil {
		return nil, err
	}

	a := &domain.Account{
		Email: p.Email, DisplayName: p.DisplayName,
		OAuth2Provider: p.Provider, OAuth2TokenEncrypted: encToken, IsActive: true,
		ConnectorType: p.ConnectorType,
	}

	switch p.ConnectorType {
	case domain.ConnectorGmailAPI:
		a.IMAPHost = gmailAPIPlaceholderIMAPHost
	case domain.ConnectorMSGraph:
		a.IMAPHost = msgraphPlaceholderIMAPHost
	default:
		a.IMAPHost = p.IMAPHost
		a.IMAPPort = p.IMAPPort
		if a.IMAPPort == 0 {
			a.IMAPPort = defaultPortFor(p.IMAPTLS)
		}
		a.IMAPTLS = p.IMAPTLS
		a.IMAPUsername = p.IMAPUsername
		if a.IMAPUsername == "" {
			a.IMAPUsername = p.Email
		}
	}

	id, err := m.repo.Create(ctx, a)
	if err != nil {
		return nil, err
	}
	return m.repo.GetByID(ctx, id)
}

// DecryptOAuth2Token returns a's plaintext OAuth2 token. a.OAuth2Provider
// must be non-empty (this isn't an OAuth2 account otherwise).
func (m *Manager) DecryptOAuth2Token(a *domain.Account) (oauth2pkg.Token, error) {
	plain, err := crypto.Decrypt(m.key, a.OAuth2TokenEncrypted, []byte(a.Email))
	if err != nil {
		return oauth2pkg.Token{}, fmt.Errorf("account: decrypting oauth2 token: %w", err)
	}
	var tok oauth2pkg.Token
	if err := json.Unmarshal(plain, &tok); err != nil {
		return oauth2pkg.Token{}, fmt.Errorf("account: unmarshaling oauth2 token: %w", err)
	}
	return tok, nil
}

// UpdateOAuth2Token re-encrypts and persists a refreshed token, without
// touching any of the account's other fields — the narrow write a token
// refresh needs, as opposed to UpdateAccount's full-row replace.
func (m *Manager) UpdateOAuth2Token(ctx context.Context, a *domain.Account, tok oauth2pkg.Token) error {
	enc, err := m.encryptOAuth2Token(a.Email, tok)
	if err != nil {
		return err
	}
	return m.repo.UpdateOAuth2Token(ctx, a.ID, enc)
}

func (m *Manager) encryptOAuth2Token(email string, tok oauth2pkg.Token) ([]byte, error) {
	data, err := json.Marshal(tok)
	if err != nil {
		return nil, fmt.Errorf("account: marshaling oauth2 token: %w", err)
	}
	enc, err := crypto.Encrypt(m.key, data, []byte(email))
	if err != nil {
		return nil, fmt.Errorf("account: encrypting oauth2 token: %w", err)
	}
	return enc, nil
}

// OAuth2TokenRefresher refreshes a's OAuth2 provider's token — satisfied
// structurally by internal/oauth2config.Manager's RefreshToken method,
// without account importing that package (which itself imports account
// for CredentialSubkey — an import back the other way would cycle).
type OAuth2TokenRefresher interface {
	RefreshToken(ctx context.Context, provider string, current oauth2pkg.Token) (oauth2pkg.Token, error)
}

// IMAPAuth is ResolveIMAPAuth's result: exactly one of Password or
// OAuth2AccessToken is set, matching imapclient.ConnectOptions' own
// either/or Password/OAuth2AccessToken fields.
type IMAPAuth struct {
	Password          string
	OAuth2AccessToken string
}

// ResolveIMAPAuth returns whatever imapclient.Connect needs to
// authenticate as a: the decrypted plain password for a regular account,
// or a valid (refreshed if necessary) OAuth2 access token for an OAuth2
// account. refresher may be nil — an expired OAuth2 token is then
// returned as-is, which will simply fail IMAP login with a clear
// "authentication failed" rather than refreshing first; callers that
// can supply a refresher (anywhere internal/oauth2config.Manager is
// available) should.
func (m *Manager) ResolveIMAPAuth(ctx context.Context, a *domain.Account, refresher OAuth2TokenRefresher) (IMAPAuth, error) {
	if a.OAuth2Provider == "" {
		password, err := m.DecryptPassword(a)
		if err != nil {
			return IMAPAuth{}, err
		}
		return IMAPAuth{Password: password}, nil
	}

	tok, err := m.resolveOAuth2Token(ctx, a, refresher)
	if err != nil {
		return IMAPAuth{}, err
	}
	return IMAPAuth{OAuth2AccessToken: tok.AccessToken}, nil
}

// ResolveGmailAPIAccessToken is ResolveIMAPAuth's counterpart for a
// ConnectorGmailAPI account: every such account authenticates purely via
// OAuth2 (there's no IMAP password branch to consider — a Gmail API
// account is never anything else), so this just returns the resolved,
// refreshed-if-necessary access token internal/gmailapi.Client needs.
func (m *Manager) ResolveGmailAPIAccessToken(ctx context.Context, a *domain.Account, refresher OAuth2TokenRefresher) (string, error) {
	tok, err := m.resolveOAuth2Token(ctx, a, refresher)
	if err != nil {
		return "", err
	}
	return tok.AccessToken, nil
}

// ResolveMSGraphAccessToken is ResolveIMAPAuth's counterpart for a
// ConnectorMSGraph account: every such account authenticates purely via
// OAuth2 (there's no IMAP password branch to consider), so this just
// returns the resolved, refreshed-if-necessary access token
// internal/msgraph.Client needs — identical in shape to
// ResolveGmailAPIAccessToken, just for the other connector.
func (m *Manager) ResolveMSGraphAccessToken(ctx context.Context, a *domain.Account, refresher OAuth2TokenRefresher) (string, error) {
	tok, err := m.resolveOAuth2Token(ctx, a, refresher)
	if err != nil {
		return "", err
	}
	return tok.AccessToken, nil
}

// resolveOAuth2Token decrypts a's stored OAuth2 token and refreshes it
// first if it's expired and a refresher was supplied — the shared dance
// both ResolveIMAPAuth and ResolveGmailAPIAccessToken need. refresher may
// be nil, in which case an expired token is returned as-is (see
// ResolveIMAPAuth's own doc comment for why: the caller then simply gets
// a clear "authentication failed" from whatever it tries to use the
// token for, rather than this silently refreshing when the caller didn't
// ask it to).
func (m *Manager) resolveOAuth2Token(ctx context.Context, a *domain.Account, refresher OAuth2TokenRefresher) (oauth2pkg.Token, error) {
	tok, err := m.DecryptOAuth2Token(a)
	if err != nil {
		return oauth2pkg.Token{}, err
	}
	if !tok.Expired() || refresher == nil {
		return tok, nil
	}
	refreshed, err := refresher.RefreshToken(ctx, a.OAuth2Provider, tok)
	if err != nil {
		return oauth2pkg.Token{}, fmt.Errorf("account: refreshing oauth2 token: %w", err)
	}
	if err := m.UpdateOAuth2Token(ctx, a, refreshed); err != nil {
		return oauth2pkg.Token{}, fmt.Errorf("account: persisting refreshed oauth2 token: %w", err)
	}
	return refreshed, nil
}

func validateAddOAuth2AccountParams(p AddOAuth2AccountParams) error {
	if strings.TrimSpace(p.Email) == "" {
		return fmt.Errorf("account: email is required")
	}
	// A ConnectorGmailAPI/ConnectorMSGraph account never connects via
	// IMAP at all (see gmailAPIPlaceholderIMAPHost/msgraphPlaceholderIMAPHost)
	// — none of the IMAP-specific fields below apply to it.
	if p.ConnectorType != domain.ConnectorGmailAPI && p.ConnectorType != domain.ConnectorMSGraph {
		if strings.TrimSpace(p.IMAPHost) == "" {
			return fmt.Errorf("account: imap host is required")
		}
		if p.IMAPPort < 0 || p.IMAPPort > 65535 {
			return fmt.Errorf("account: imap port must be between 0 and 65535, got %d", p.IMAPPort)
		}
	}
	if p.Provider != domain.OAuth2ProviderGoogle && p.Provider != domain.OAuth2ProviderMicrosoft {
		return fmt.Errorf("account: unknown oauth2 provider %q", p.Provider)
	}
	if p.ConnectorType == domain.ConnectorGmailAPI && p.Provider != domain.OAuth2ProviderGoogle {
		return fmt.Errorf("account: connector_type gmail_api requires oauth2_provider google")
	}
	if p.ConnectorType == domain.ConnectorMSGraph && p.Provider != domain.OAuth2ProviderMicrosoft {
		return fmt.Errorf("account: connector_type ms_graph requires oauth2_provider microsoft")
	}
	if p.Token.AccessToken == "" {
		return fmt.Errorf("account: oauth2 access token is required")
	}
	return nil
}
