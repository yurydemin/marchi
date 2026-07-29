package scheduler

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/yurydemin/marchi/internal/account"
	"github.com/yurydemin/marchi/internal/domain"
	"github.com/yurydemin/marchi/internal/gmailapi"
	oauth2pkg "github.com/yurydemin/marchi/internal/oauth2"
)

// startFakeGmailServer is a minimal stand-in for the Gmail REST API —
// just enough for syncOne's gmail_api dispatch path to complete a full
// sync of one message, matching this project's OAuth2-mechanics-only
// testing policy (no real Google account involved; see
// internal/gmailapi's own tests for the same approach).
func startFakeGmailServer(t *testing.T) string {
	t.Helper()
	raw := "Message-Id: <dispatch-test@example.com>\r\n" +
		"Subject: dispatch test\r\n" +
		"From: sender@example.com\r\n" +
		"Date: Mon, 2 Jan 2006 15:04:05 +0000\r\n\r\n" +
		"body\r\n"

	mux := http.NewServeMux()
	mux.HandleFunc("/users/me/profile", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(gmailapi.Profile{EmailAddress: "user@gmail.com", HistoryID: "42"})
	})
	mux.HandleFunc("/users/me/messages", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(gmailapi.MessageList{Messages: []gmailapi.MessageRef{{ID: "m1"}}})
	})
	mux.HandleFunc("/users/me/messages/m1", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(gmailapi.Message{ID: "m1", Raw: base64.RawURLEncoding.EncodeToString([]byte(raw))})
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestScheduler_SyncOne_DispatchesGmailAPIAccountsToGmailSync guards the
// branch added for ConnectorGmailAPI accounts: syncOne must resolve an
// OAuth2 access token and call SyncAccountGmailAPI (Gmail REST API),
// never DecryptPassword/SyncAccount (IMAP) — those would fail outright
// for an account that has no IMAP password to decrypt in the first
// place.
func TestScheduler_SyncOne_DispatchesGmailAPIAccountsToGmailSync(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	id, err := env.deps.AccountsRepo.Create(ctx, &domain.Account{
		Email: "user@gmail.com", IsActive: true,
		OAuth2Provider: domain.OAuth2ProviderGoogle,
		ConnectorType:  domain.ConnectorGmailAPI,
	})
	if err != nil {
		t.Fatalf("creating account: %v", err)
	}
	a, err := env.deps.AccountsRepo.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if err := env.deps.Manager.UpdateOAuth2Token(ctx, a, oauth2pkg.Token{
		AccessToken: "fake-access-token", Expiry: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("UpdateOAuth2Token: %v", err)
	}

	env.deps.GmailAPIBaseURL = startFakeGmailServer(t)
	s := newTestScheduler(t, env)

	s.syncOne(id, "test-job-id")

	logs, err := env.deps.SyncLogsRepo.ListByAccount(ctx, id, 1)
	if err != nil {
		t.Fatalf("ListByAccount (sync_logs): %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 sync_logs row, got %d", len(logs))
	}
	if logs[0].Status != domain.SyncLogCompleted {
		t.Errorf("Status = %q, want %q; error: %q", logs[0].Status, domain.SyncLogCompleted, logs[0].ErrorMsg)
	}

	emails, err := env.deps.EmailsRepo.ListByAccount(ctx, id)
	if err != nil {
		t.Fatalf("ListByAccount (emails): %v", err)
	}
	if len(emails) != 1 {
		t.Fatalf("archived %d emails via the gmail_api dispatch path, want 1", len(emails))
	}

	a, err = env.deps.AccountsRepo.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID after sync: %v", err)
	}
	if a.GmailHistoryID != "42" {
		t.Errorf("GmailHistoryID = %q, want 42 (persisted by SyncAccountGmailAPI)", a.GmailHistoryID)
	}
}

// TestScheduler_SyncOne_IMAPAccountsStillUseIMAPPath is the dispatch
// branch's control: an ordinary (default ConnectorType) account must be
// completely unaffected by the gmail_api branch's existence.
func TestScheduler_SyncOne_IMAPAccountsStillUseIMAPPath(t *testing.T) {
	env := newTestEnv(t)
	a, err := env.deps.Manager.AddAccount(context.Background(), account.AddAccountParams{
		Email: "imap-user@example.com", IMAPHost: "127.0.0.1", IMAPPort: 1, // nothing listens here
		IMAPTLS: domain.IMAPTLSNone, IMAPPassword: "hunter2hunter2",
	})
	if err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	s := newTestScheduler(t, env)

	s.syncOne(a.ID, "test-job-id")
	id := a.ID

	logs, err := env.deps.SyncLogsRepo.ListByAccount(context.Background(), id, 1)
	if err != nil {
		t.Fatalf("ListByAccount: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 sync_logs row, got %d", len(logs))
	}
	if logs[0].Status != domain.SyncLogFailed {
		t.Errorf("Status = %q, want %q (connection to an unreachable IMAP host should fail)", logs[0].Status, domain.SyncLogFailed)
	}
}
