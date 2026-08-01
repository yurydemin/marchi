package sync

import (
	"context"
	"net"
	"strconv"
	"testing"

	"github.com/yurydemin/marchi/internal/db/repo"
	"github.com/yurydemin/marchi/internal/domain"
)

// TestFetchNewMessages_RecordsRuleMatchesInStats guards the P2-15 stats
// wiring at the FetchStats level: every message a rule actually matches
// (regardless of whether the resulting action is skip or archive) must
// increment that rule's entry in stats.RuleMatches, and a message that
// matches no active rule must not add any entry at all.
func TestFetchNewMessages_RecordsRuleMatchesInStats(t *testing.T) {
	env := newFetchTestEnv(t)

	addr := startFakeFetchServer(t, fakeFetchServer{
		uidValidity: 1001,
		uidNext:     4,
		messages: []fakeMessage{
			{uid: 1, flags: "", body: testEmail("Weekly newsletter")},
			{uid: 2, flags: "", body: testEmail("Weekly newsletter digest")},
			{uid: 3, flags: "", body: testEmail("Important invoice")},
		},
	})
	c := connectToFakeServer(t, addr)
	defer c.Logout()

	folder, err := env.foldersR.UpsertFolder(context.Background(), env.accountID, "INBOX", 1001)
	if err != nil {
		t.Fatalf("UpsertFolder: %v", err)
	}
	mw := env.newWriter(t, "INBOX")

	skipRule := skipNewsletterRule()
	skipRule.ID = 42
	activeRules := []*domain.Rule{skipRule}

	stats, err := FetchNewMessages(context.Background(), c, env.accountID, folder, mw, env.w, env.emailsR, env.foldersR, env.attachmentsR, nil, nil, activeRules, nil)
	if err != nil {
		t.Fatalf("FetchNewMessages: %v", err)
	}
	if got := stats.RuleMatches[42]; got != 2 {
		t.Errorf("RuleMatches[42] = %d, want 2 (both newsletter subjects matched)", got)
	}
	if len(stats.RuleMatches) != 1 {
		t.Errorf("RuleMatches = %+v, want exactly one entry (the invoice matched no rule)", stats.RuleMatches)
	}
}

// TestSyncAccount_RecordsRuleMatchesViaRulesRepo is the end-to-end
// integration test: a real RulesRepo-backed rule's match_count and
// last_matched_at must reflect activity after a full SyncAccount run,
// not just what FetchStats accumulated in memory.
func TestSyncAccount_RecordsRuleMatchesViaRulesRepo(t *testing.T) {
	env := newFetchTestEnv(t)
	rulesR := repo.NewRulesRepo(env.sqlDB, env.w)
	syncLogsR := repo.NewSyncLogsRepo(env.sqlDB, env.w)

	ruleID, err := rulesR.Create(context.Background(), &domain.Rule{
		Name: "skip newsletters", Priority: 0, IsActive: true,
		Action:     domain.ActionSkip,
		Conditions: domain.RuleNode{Type: domain.ConditionSubjectContains, Value: "(?i)newsletter"},
	})
	if err != nil {
		t.Fatalf("creating rule: %v", err)
	}

	addr := startFakeFetchServer(t, fakeFetchServer{
		uidValidity: 1001,
		uidNext:     3,
		messages: []fakeMessage{
			{uid: 1, flags: "", body: testEmail("Weekly newsletter")},
			{uid: 2, flags: "", body: testEmail("Important invoice")},
		},
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
		ID: env.accountID, Email: "user@example.com",
		IMAPHost: host, IMAPPort: port, IMAPTLS: domain.IMAPTLSNone,
	}

	if _, err := SyncAccount(context.Background(), account, "pass", "", env.maildirRoot, "test-host",
		env.w, env.foldersR, env.emailsR, env.attachmentsR, syncLogsR, rulesR, nil, nil, nil, nil); err != nil {
		t.Fatalf("SyncAccount: %v", err)
	}

	rule, err := rulesR.GetByID(context.Background(), ruleID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if rule.MatchCount != 1 {
		t.Errorf("MatchCount = %d, want 1", rule.MatchCount)
	}
	if rule.LastMatchedAt.IsZero() {
		t.Error("LastMatchedAt was not set")
	}
}
