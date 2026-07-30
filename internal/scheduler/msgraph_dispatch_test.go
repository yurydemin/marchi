package scheduler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/yurydemin/marchi/internal/domain"
	"github.com/yurydemin/marchi/internal/msgraph"
	oauth2pkg "github.com/yurydemin/marchi/internal/oauth2"
)

// startFakeMSGraphServer is a minimal stand-in for the Microsoft Graph
// REST API — just enough for syncOne's ms_graph dispatch path to
// complete a full sync of one message, matching this project's
// OAuth2-mechanics-only testing policy (no real Microsoft tenant
// involved; see internal/msgraph's own tests for the same approach).
func startFakeMSGraphServer(t *testing.T) string {
	t.Helper()
	raw := "Message-Id: <dispatch-test@example.com>\r\n" +
		"Subject: dispatch test\r\n" +
		"From: sender@example.com\r\n" +
		"Date: Mon, 2 Jan 2006 15:04:05 +0000\r\n\r\n" +
		"body\r\n"

	mux := http.NewServeMux()
	mux.HandleFunc("/me/mailFolders", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"value": []msgraph.MailFolder{{ID: "f-inbox", DisplayName: "Inbox"}},
		})
	})
	mux.HandleFunc("/me/mailFolders/f-inbox/messages/delta", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"value":            []msgraph.MessageStub{{ID: "m1"}},
			"@odata.deltaLink": "http://" + r.Host + "/me/mailFolders/f-inbox/messages/delta?$deltatoken=1",
		})
	})
	mux.HandleFunc("/me/messages/m1/$value", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(raw))
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestScheduler_SyncOne_DispatchesMSGraphAccountsToMSGraphSync guards the
// branch added for ConnectorMSGraph accounts: syncOne must resolve an
// OAuth2 access token and call SyncAccountMSGraph (Microsoft Graph REST
// API), never DecryptPassword/SyncAccount (IMAP) — those would fail
// outright for an account that has no IMAP password to decrypt in the
// first place.
func TestScheduler_SyncOne_DispatchesMSGraphAccountsToMSGraphSync(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	id, err := env.deps.AccountsRepo.Create(ctx, &domain.Account{
		Email: "user@outlook.com", IsActive: true,
		OAuth2Provider: domain.OAuth2ProviderMicrosoft,
		ConnectorType:  domain.ConnectorMSGraph,
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

	env.deps.MSGraphBaseURL = startFakeMSGraphServer(t)
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
		t.Fatalf("archived %d emails via the ms_graph dispatch path, want 1", len(emails))
	}
}
