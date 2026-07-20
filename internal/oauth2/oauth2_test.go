package oauth2

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/yurydemin/marchi/internal/domain"
)

// fakeTokenServer emulates a provider's /token endpoint just enough to
// exercise Exchange/Refresh (FR-AM-01's "authorization-code exchange,
// refresh" — the mechanics-only testing policy this project settled on
// for OAuth2, no live consent screen). It hands back a fixed token,
// remembering the last request's form values so tests can assert on
// what Exchange/Refresh actually sent.
type fakeTokenServer struct {
	*httptest.Server
	lastForm     url.Values
	responseCode int
	response     map[string]any
}

func newFakeTokenServer(t *testing.T) *fakeTokenServer {
	t.Helper()
	f := &fakeTokenServer{
		responseCode: http.StatusOK,
		response: map[string]any{
			"access_token": "fake-access-token", "token_type": "Bearer",
			"refresh_token": "fake-refresh-token", "expires_in": 3600, "scope": "https://mail.google.com/",
		},
	}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parsing form: %v", err)
		}
		f.lastForm = r.Form
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(f.responseCode)
		_ = json.NewEncoder(w).Encode(f.response)
	}))
	t.Cleanup(f.Close)
	return f
}

// withFakeGoogleEndpoint points the package-level googleEndpoint at a
// fake server for the duration of the test, restoring the real one
// afterward — the simplest seam for testing Exchange/Refresh's actual
// HTTP behavior without reaching Google.
func withFakeGoogleEndpoint(t *testing.T, tokenURL string) {
	t.Helper()
	original := googleEndpoint
	googleEndpoint = oauth2.Endpoint{AuthURL: original.AuthURL, TokenURL: tokenURL}
	t.Cleanup(func() { googleEndpoint = original })
}

func testApp() App {
	return App{
		Provider: domain.OAuth2ProviderGoogle, ClientID: "test-client-id",
		ClientSecret: "test-client-secret", RedirectURL: "https://mailvault.local/oauth2/google/callback",
	}
}

func TestApp_AuthURL_IncludesClientIDStateAndOfflineAccess(t *testing.T) {
	app := testApp()
	authURL, err := app.AuthURL("random-csrf-state")
	if err != nil {
		t.Fatalf("AuthURL: %v", err)
	}
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("parsing AuthURL result: %v", err)
	}
	q := u.Query()
	if q.Get("client_id") != "test-client-id" {
		t.Errorf("client_id = %q, want test-client-id", q.Get("client_id"))
	}
	if q.Get("state") != "random-csrf-state" {
		t.Errorf("state = %q, want random-csrf-state", q.Get("state"))
	}
	if q.Get("access_type") != "offline" {
		t.Errorf("access_type = %q, want offline (required for a refresh_token to come back)", q.Get("access_type"))
	}
	if q.Get("prompt") != "consent" {
		t.Errorf("prompt = %q, want consent", q.Get("prompt"))
	}
}

func TestApp_Exchange_AgainstFakeTokenEndpoint(t *testing.T) {
	srv := newFakeTokenServer(t)
	withFakeGoogleEndpoint(t, srv.URL)

	app := testApp()
	tok, err := app.Exchange(context.Background(), "fake-authorization-code")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if tok.AccessToken != "fake-access-token" {
		t.Errorf("AccessToken = %q, want fake-access-token", tok.AccessToken)
	}
	if tok.RefreshToken != "fake-refresh-token" {
		t.Errorf("RefreshToken = %q, want fake-refresh-token", tok.RefreshToken)
	}
	if tok.Expiry.Before(time.Now().Add(59 * time.Minute)) {
		t.Errorf("Expiry = %v, want ~1 hour from now (expires_in=3600)", tok.Expiry)
	}

	if srv.lastForm.Get("code") != "fake-authorization-code" {
		t.Errorf("token request 'code' = %q, want fake-authorization-code", srv.lastForm.Get("code"))
	}
	if srv.lastForm.Get("grant_type") != "authorization_code" {
		t.Errorf("token request 'grant_type' = %q, want authorization_code", srv.lastForm.Get("grant_type"))
	}
}

func TestApp_Exchange_ProviderRejectsCode_ReturnsError(t *testing.T) {
	srv := newFakeTokenServer(t)
	srv.responseCode = http.StatusBadRequest
	srv.response = map[string]any{"error": "invalid_grant", "error_description": "code already used"}
	withFakeGoogleEndpoint(t, srv.URL)

	app := testApp()
	if _, err := app.Exchange(context.Background(), "already-used-code"); err == nil {
		t.Error("Exchange with a rejected code succeeded, want an error")
	}
}

func TestApp_Refresh_AgainstFakeTokenEndpoint(t *testing.T) {
	srv := newFakeTokenServer(t)
	withFakeGoogleEndpoint(t, srv.URL)

	app := testApp()
	current := Token{AccessToken: "old-access-token", RefreshToken: "existing-refresh-token", Expiry: time.Now().Add(-time.Hour)}
	refreshed, err := app.Refresh(context.Background(), current)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if refreshed.AccessToken != "fake-access-token" {
		t.Errorf("AccessToken = %q, want fake-access-token (the refreshed value)", refreshed.AccessToken)
	}
	if srv.lastForm.Get("grant_type") != "refresh_token" {
		t.Errorf("token request 'grant_type' = %q, want refresh_token", srv.lastForm.Get("grant_type"))
	}
	if srv.lastForm.Get("refresh_token") != "existing-refresh-token" {
		t.Errorf("token request 'refresh_token' = %q, want existing-refresh-token", srv.lastForm.Get("refresh_token"))
	}
}

// TestApp_Refresh_MissingNewRefreshToken_KeepsExisting covers providers
// that don't return a fresh refresh_token on every refresh (common) —
// the caller must not lose the one it already had.
func TestApp_Refresh_MissingNewRefreshToken_KeepsExisting(t *testing.T) {
	srv := newFakeTokenServer(t)
	srv.response = map[string]any{"access_token": "new-access-token", "token_type": "Bearer", "expires_in": 3600}
	withFakeGoogleEndpoint(t, srv.URL)

	app := testApp()
	current := Token{AccessToken: "old", RefreshToken: "must-survive", Expiry: time.Now().Add(-time.Hour)}
	refreshed, err := app.Refresh(context.Background(), current)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if refreshed.RefreshToken != "must-survive" {
		t.Errorf("RefreshToken = %q, want must-survive (kept from the previous token)", refreshed.RefreshToken)
	}
}

func TestToken_Expired(t *testing.T) {
	cases := []struct {
		name   string
		expiry time.Time
		want   bool
	}{
		{"zero expiry never expires", time.Time{}, false},
		{"far future", time.Now().Add(time.Hour), false},
		{"already past", time.Now().Add(-time.Minute), true},
		{"within the early-refresh margin", time.Now().Add(10 * time.Second), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tok := Token{Expiry: c.expiry}
			if got := tok.Expired(); got != c.want {
				t.Errorf("Expired() = %v, want %v", got, c.want)
			}
		})
	}
}

func TestXOAUTH2InitialResponse_Format(t *testing.T) {
	got := string(XOAUTH2InitialResponse("user@example.com", "ya29.fake-token"))
	want := "user=user@example.com\x01auth=Bearer ya29.fake-token\x01\x01"
	if got != want {
		t.Errorf("XOAUTH2InitialResponse = %q, want %q", got, want)
	}
}
