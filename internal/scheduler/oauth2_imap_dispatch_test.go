package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/yurydemin/marchi/internal/account"
	"github.com/yurydemin/marchi/internal/domain"
	oauth2pkg "github.com/yurydemin/marchi/internal/oauth2"
)

// TestScheduler_SyncOne_OAuth2IMAPAccount_ResolvesTokenBeforeConnecting is a
// regression test for a real bug: syncOne used to call
// Manager.DecryptPassword unconditionally, even for an OAuth2 IMAP account
// (which has no IMAP password at all — accounts.imap_password_encrypted is
// never set for it). That failed immediately with a decrypt error, and
// crucially, that failure happened *before* SyncAccount (and therefore
// syncLogsRepo.Start) was ever called — so a sync attempt against an
// OAuth2 account silently left zero sync_logs rows behind, with nothing
// in the UI/API to explain why the account never seemed to sync.
//
// After the fix, syncOne calls Manager.ResolveIMAPAuth, which correctly
// resolves an OAuth2 account's access token instead of trying to decrypt
// a nonexistent password — this test's account points at an unreachable
// host (port 1), so the sync itself still fails, but it must fail *inside*
// SyncAccount (a connection error, recorded as a sync_logs row), not
// before ever reaching it.
func TestScheduler_SyncOne_OAuth2IMAPAccount_ResolvesTokenBeforeConnecting(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	a, err := env.deps.Manager.AddOAuth2Account(ctx, account.AddOAuth2AccountParams{
		Email: "oauth2user@gmail.com", IMAPHost: "127.0.0.1", IMAPPort: 1, // nothing listens here
		IMAPTLS: domain.IMAPTLSNone, Provider: domain.OAuth2ProviderGoogle,
		Token: oauth2pkg.Token{AccessToken: "ya29.valid-token", Expiry: time.Now().Add(time.Hour)},
	})
	if err != nil {
		t.Fatalf("AddOAuth2Account: %v", err)
	}

	s := newTestScheduler(t, env)

	done := make(chan struct{})
	go func() {
		s.syncOne(a.ID, "test-job-id")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("syncOne did not return within 10s against an unreachable host")
	}

	logs, err := env.deps.SyncLogsRepo.ListByAccount(ctx, a.ID, 1)
	if err != nil {
		t.Fatalf("ListByAccount: %v", err)
	}
	// Before the fix, this would be 0: DecryptPassword's error on an
	// OAuth2 account (no password ever set) made syncOne return before
	// SyncAccount — and therefore syncLogsRepo.Start — was ever called.
	if len(logs) != 1 {
		t.Fatalf("expected syncOne to have recorded a sync_logs row (proving it reached SyncAccount), got %d", len(logs))
	}
	if logs[0].Status != domain.SyncLogFailed {
		t.Errorf("Status = %q, want %q (connection to an unreachable host should fail)", logs[0].Status, domain.SyncLogFailed)
	}
}

// TestScheduler_SyncOne_OAuth2IMAPAccount_RefreshesExpiredToken confirms
// syncOne's ResolveIMAPAuth call actually uses Deps.OAuth2Refresher to
// refresh an expired token (rather than, say, silently passing along a
// stale one) — a fake refresher records whether it was invoked.
func TestScheduler_SyncOne_OAuth2IMAPAccount_RefreshesExpiredToken(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	a, err := env.deps.Manager.AddOAuth2Account(ctx, account.AddOAuth2AccountParams{
		Email: "expired@gmail.com", IMAPHost: "127.0.0.1", IMAPPort: 1,
		IMAPTLS: domain.IMAPTLSNone, Provider: domain.OAuth2ProviderGoogle,
		Token: oauth2pkg.Token{AccessToken: "stale-token", Expiry: time.Now().Add(-time.Hour)},
	})
	if err != nil {
		t.Fatalf("AddOAuth2Account: %v", err)
	}

	refresher := &fakeOAuth2Refresher{
		token: oauth2pkg.Token{AccessToken: "fresh-token", Expiry: time.Now().Add(time.Hour)},
	}
	env.deps.OAuth2Refresher = refresher
	s := newTestScheduler(t, env)

	s.syncOne(a.ID, "test-job-id")

	if !refresher.called {
		t.Error("expected OAuth2Refresher.RefreshToken to have been called for an expired token")
	}

	refreshed, err := env.deps.AccountsRepo.GetByID(ctx, a.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	tok, err := env.deps.Manager.DecryptOAuth2Token(refreshed)
	if err != nil {
		t.Fatalf("DecryptOAuth2Token: %v", err)
	}
	if tok.AccessToken != "fresh-token" {
		t.Errorf("persisted AccessToken = %q, want fresh-token (the refreshed value)", tok.AccessToken)
	}
}

type fakeOAuth2Refresher struct {
	called bool
	token  oauth2pkg.Token
}

func (f *fakeOAuth2Refresher) RefreshToken(ctx context.Context, provider string, current oauth2pkg.Token) (oauth2pkg.Token, error) {
	f.called = true
	return f.token, nil
}
