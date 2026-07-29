package sync

import (
	"context"
	"net"
	"strconv"
	"testing"

	"github.com/yurydemin/marchi/internal/db/repo"
	"github.com/yurydemin/marchi/internal/domain"
)

// TestSyncAccount_CancelledContext_RecordsCancelledStatus covers NFR-RL-05
// end to end at the SyncAccount level: a shutdown-cancelled context must
// produce a sync_logs row with status "cancelled", distinct from "failed"
// — a deliberate stop isn't the same thing as a real error.
func TestSyncAccount_CancelledContext_RecordsCancelledStatus(t *testing.T) {
	env := newFetchTestEnv(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	account := &domain.Account{
		ID: env.accountID, Email: "user@example.com",
		IMAPHost: "127.0.0.1", IMAPPort: 143, IMAPTLS: domain.IMAPTLSNone,
	}
	syncLogsRepo := repo.NewSyncLogsRepo(env.sqlDB, env.w)

	_, err := SyncAccount(ctx, account, "pass", "", env.maildirRoot, "test-host",
		env.w, env.foldersR, env.emailsR, env.attachmentsR, syncLogsRepo, nil, nil, nil, nil, nil)
	if err == nil {
		t.Fatal("expected an error for an already-cancelled context, got nil")
	}

	logs, listErr := syncLogsRepo.ListByAccount(context.Background(), env.accountID, 1)
	if listErr != nil {
		t.Fatalf("ListByAccount: %v", listErr)
	}
	if len(logs) != 1 {
		t.Fatalf("got %d sync_logs rows, want 1", len(logs))
	}
	if logs[0].Status != domain.SyncLogCancelled {
		t.Errorf("Status = %q, want %q", logs[0].Status, domain.SyncLogCancelled)
	}
}

// TestSyncAccount_OAuth2AccessToken_AuthenticatesViaXOAUTH2NotLogin is a
// regression test for a real bug: every real call site that resolves an
// account's IMAP credentials before calling SyncAccount (scheduler,
// cmd_sync, the accounts test-connection handlers) used to call
// DecryptPassword unconditionally, never checking whether the account was
// actually an OAuth2 account (a.OAuth2Provider != "") and never calling
// account.Manager.ResolveIMAPAuth — so an OAuth2 IMAP account's sync would
// always try LOGIN with an empty/garbage password instead of XOAUTH2. This
// proves the fix at the level where it actually matters: SyncAccount's
// oauth2AccessToken parameter must reach imapclient.ConnectOptions and
// drive AUTHENTICATE XOAUTH2 — the fake server here rejects LOGIN
// outright (simulating a provider with basic auth disabled, the exact
// real-world scenario this bug broke), so the sync can only succeed if
// XOAUTH2 was actually used.
func TestSyncAccount_OAuth2AccessToken_AuthenticatesViaXOAUTH2NotLogin(t *testing.T) {
	env := newFetchTestEnv(t)

	addr := startFakeFetchServer(t, fakeFetchServer{
		uidValidity: 1, uidNext: 2, rejectLogin: true,
		messages: []fakeMessage{{uid: 1, body: testEmail("oauth2-synced")}},
	})
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}

	account := &domain.Account{
		ID: env.accountID, Email: "user@gmail.com",
		IMAPHost: host, IMAPPort: port, IMAPTLS: domain.IMAPTLSNone,
		IMAPUsername: "user@gmail.com", OAuth2Provider: domain.OAuth2ProviderGoogle,
	}
	syncLogsRepo := repo.NewSyncLogsRepo(env.sqlDB, env.w)

	results, err := SyncAccount(context.Background(), account, "" /* no password — this is an OAuth2 account */, "ya29.fake-access-token",
		env.maildirRoot, "test-host", env.w, env.foldersR, env.emailsR, env.attachmentsR, syncLogsRepo, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("SyncAccount: %v (LOGIN is rejected by the fake server — this must succeed via XOAUTH2)", err)
	}

	total := 0
	for _, r := range results {
		total += r.Fetched
	}
	if total != 1 {
		t.Errorf("archived %d message(s), want 1", total)
	}
}
