package account

import (
	"context"
	"testing"
	"time"

	"github.com/yurydemin/marchi/internal/domain"
	oauth2pkg "github.com/yurydemin/marchi/internal/oauth2"
)

func TestAddOAuth2Account_TokenIsEncryptedAtRest(t *testing.T) {
	mgr := openTestManager(t, testMasterKey(t))
	ctx := context.Background()

	tok := oauth2pkg.Token{AccessToken: "ya29.access-token", RefreshToken: "refresh-token", Expiry: time.Now().Add(time.Hour)}
	a, err := mgr.AddOAuth2Account(ctx, AddOAuth2AccountParams{
		Email: "user@gmail.com", IMAPHost: "imap.gmail.com", IMAPTLS: domain.IMAPTLSSSL,
		Provider: domain.OAuth2ProviderGoogle, Token: tok,
	})
	if err != nil {
		t.Fatalf("AddOAuth2Account: %v", err)
	}
	if a.OAuth2Provider != domain.OAuth2ProviderGoogle {
		t.Errorf("OAuth2Provider = %q, want google", a.OAuth2Provider)
	}
	if a.IMAPPasswordEncrypted != nil {
		t.Errorf("IMAPPasswordEncrypted = %v, want nil (OAuth2 accounts have no password)", a.IMAPPasswordEncrypted)
	}
	if len(a.OAuth2TokenEncrypted) == 0 {
		t.Fatal("OAuth2TokenEncrypted is empty")
	}

	got, err := mgr.DecryptOAuth2Token(a)
	if err != nil {
		t.Fatalf("DecryptOAuth2Token: %v", err)
	}
	if got.AccessToken != tok.AccessToken || got.RefreshToken != tok.RefreshToken {
		t.Errorf("decrypted token = %+v, want %+v", got, tok)
	}
}

func TestAddOAuth2Account_DefaultsUsernameAndPort(t *testing.T) {
	mgr := openTestManager(t, testMasterKey(t))
	a, err := mgr.AddOAuth2Account(context.Background(), AddOAuth2AccountParams{
		Email: "user@outlook.com", IMAPHost: "outlook.office365.com", IMAPTLS: domain.IMAPTLSSSL,
		Provider: domain.OAuth2ProviderMicrosoft, Token: oauth2pkg.Token{AccessToken: "tok"},
	})
	if err != nil {
		t.Fatalf("AddOAuth2Account: %v", err)
	}
	if a.IMAPUsername != "user@outlook.com" {
		t.Errorf("IMAPUsername = %q, want user@outlook.com (defaulted from Email)", a.IMAPUsername)
	}
	if a.IMAPPort != 993 {
		t.Errorf("IMAPPort = %d, want 993 (default for TLS)", a.IMAPPort)
	}
}

func TestAddOAuth2Account_ValidationErrors(t *testing.T) {
	mgr := openTestManager(t, testMasterKey(t))
	ctx := context.Background()

	cases := []struct {
		name string
		p    AddOAuth2AccountParams
	}{
		{"missing email", AddOAuth2AccountParams{IMAPHost: "h", Provider: domain.OAuth2ProviderGoogle, Token: oauth2pkg.Token{AccessToken: "t"}}},
		{"missing host", AddOAuth2AccountParams{Email: "a@b.com", Provider: domain.OAuth2ProviderGoogle, Token: oauth2pkg.Token{AccessToken: "t"}}},
		{"unknown provider", AddOAuth2AccountParams{Email: "a@b.com", IMAPHost: "h", Provider: "yahoo", Token: oauth2pkg.Token{AccessToken: "t"}}},
		{"missing access token", AddOAuth2AccountParams{Email: "a@b.com", IMAPHost: "h", Provider: domain.OAuth2ProviderGoogle}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := mgr.AddOAuth2Account(ctx, c.p); err == nil {
				t.Error("expected a validation error, got nil")
			}
		})
	}
}

func TestUpdateOAuth2Token_ReplacesTokenOnly(t *testing.T) {
	mgr := openTestManager(t, testMasterKey(t))
	ctx := context.Background()

	a, err := mgr.AddOAuth2Account(ctx, AddOAuth2AccountParams{
		Email: "user@gmail.com", IMAPHost: "imap.gmail.com", DisplayName: "Work Gmail", IMAPTLS: domain.IMAPTLSSSL,
		Provider: domain.OAuth2ProviderGoogle, Token: oauth2pkg.Token{AccessToken: "old-token"},
	})
	if err != nil {
		t.Fatalf("AddOAuth2Account: %v", err)
	}

	newTok := oauth2pkg.Token{AccessToken: "new-token", RefreshToken: "new-refresh"}
	if err := mgr.UpdateOAuth2Token(ctx, a, newTok); err != nil {
		t.Fatalf("UpdateOAuth2Token: %v", err)
	}

	updated, err := mgr.repo.GetByID(ctx, a.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if updated.DisplayName != "Work Gmail" {
		t.Errorf("DisplayName = %q, want unchanged Work Gmail", updated.DisplayName)
	}
	got, err := mgr.DecryptOAuth2Token(updated)
	if err != nil {
		t.Fatalf("DecryptOAuth2Token: %v", err)
	}
	if got.AccessToken != "new-token" {
		t.Errorf("AccessToken = %q, want new-token", got.AccessToken)
	}
}

type fakeRefresher struct {
	called    bool
	refreshed oauth2pkg.Token
	err       error
}

func (f *fakeRefresher) RefreshToken(ctx context.Context, provider string, current oauth2pkg.Token) (oauth2pkg.Token, error) {
	f.called = true
	if f.err != nil {
		return oauth2pkg.Token{}, f.err
	}
	return f.refreshed, nil
}

func TestResolveIMAPAuth_PlainAccount_ReturnsPassword(t *testing.T) {
	mgr := openTestManager(t, testMasterKey(t))
	ctx := context.Background()
	a, err := mgr.AddAccount(ctx, AddAccountParams{
		Email: "plain@example.com", IMAPHost: "h", IMAPTLS: domain.IMAPTLSSSL, IMAPPassword: "hunter2hunter2",
	})
	if err != nil {
		t.Fatalf("AddAccount: %v", err)
	}

	auth, err := mgr.ResolveIMAPAuth(ctx, a, nil)
	if err != nil {
		t.Fatalf("ResolveIMAPAuth: %v", err)
	}
	if auth.Password != "hunter2hunter2" || auth.OAuth2AccessToken != "" {
		t.Errorf("auth = %+v, want plain password only", auth)
	}
}

func TestResolveIMAPAuth_OAuth2Account_NotExpired_ReturnsTokenWithoutRefreshing(t *testing.T) {
	mgr := openTestManager(t, testMasterKey(t))
	ctx := context.Background()
	a, err := mgr.AddOAuth2Account(ctx, AddOAuth2AccountParams{
		Email: "user@gmail.com", IMAPHost: "imap.gmail.com", IMAPTLS: domain.IMAPTLSSSL,
		Provider: domain.OAuth2ProviderGoogle, Token: oauth2pkg.Token{AccessToken: "still-valid", Expiry: time.Now().Add(time.Hour)},
	})
	if err != nil {
		t.Fatalf("AddOAuth2Account: %v", err)
	}

	refresher := &fakeRefresher{}
	auth, err := mgr.ResolveIMAPAuth(ctx, a, refresher)
	if err != nil {
		t.Fatalf("ResolveIMAPAuth: %v", err)
	}
	if auth.OAuth2AccessToken != "still-valid" || auth.Password != "" {
		t.Errorf("auth = %+v, want the still-valid access token only", auth)
	}
	if refresher.called {
		t.Error("refresher was called for a non-expired token, want no refresh attempt")
	}
}

func TestResolveIMAPAuth_OAuth2Account_Expired_RefreshesAndPersists(t *testing.T) {
	mgr := openTestManager(t, testMasterKey(t))
	ctx := context.Background()
	a, err := mgr.AddOAuth2Account(ctx, AddOAuth2AccountParams{
		Email: "user@gmail.com", IMAPHost: "imap.gmail.com", IMAPTLS: domain.IMAPTLSSSL,
		Provider: domain.OAuth2ProviderGoogle, Token: oauth2pkg.Token{AccessToken: "expired", Expiry: time.Now().Add(-time.Hour)},
	})
	if err != nil {
		t.Fatalf("AddOAuth2Account: %v", err)
	}

	refresher := &fakeRefresher{refreshed: oauth2pkg.Token{AccessToken: "refreshed-token", RefreshToken: "refreshed-refresh", Expiry: time.Now().Add(time.Hour)}}
	auth, err := mgr.ResolveIMAPAuth(ctx, a, refresher)
	if err != nil {
		t.Fatalf("ResolveIMAPAuth: %v", err)
	}
	if !refresher.called {
		t.Fatal("refresher was not called for an expired token")
	}
	if auth.OAuth2AccessToken != "refreshed-token" {
		t.Errorf("OAuth2AccessToken = %q, want refreshed-token", auth.OAuth2AccessToken)
	}

	// The refreshed token must have been persisted, not just returned.
	reloaded, err := mgr.repo.GetByID(ctx, a.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	persisted, err := mgr.DecryptOAuth2Token(reloaded)
	if err != nil {
		t.Fatalf("DecryptOAuth2Token: %v", err)
	}
	if persisted.AccessToken != "refreshed-token" {
		t.Errorf("persisted AccessToken = %q, want refreshed-token", persisted.AccessToken)
	}
}

func TestResolveIMAPAuth_OAuth2Account_Expired_NoRefresher_ReturnsStaleTokenAsIs(t *testing.T) {
	mgr := openTestManager(t, testMasterKey(t))
	ctx := context.Background()
	a, err := mgr.AddOAuth2Account(ctx, AddOAuth2AccountParams{
		Email: "user@gmail.com", IMAPHost: "imap.gmail.com", IMAPTLS: domain.IMAPTLSSSL,
		Provider: domain.OAuth2ProviderGoogle, Token: oauth2pkg.Token{AccessToken: "stale", Expiry: time.Now().Add(-time.Hour)},
	})
	if err != nil {
		t.Fatalf("AddOAuth2Account: %v", err)
	}

	auth, err := mgr.ResolveIMAPAuth(ctx, a, nil)
	if err != nil {
		t.Fatalf("ResolveIMAPAuth: %v", err)
	}
	if auth.OAuth2AccessToken != "stale" {
		t.Errorf("OAuth2AccessToken = %q, want the stale token returned as-is (no refresher available)", auth.OAuth2AccessToken)
	}
}
