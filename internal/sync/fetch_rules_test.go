package sync

import (
	"context"
	"testing"

	"github.com/yurydemin/marchi/internal/domain"
)

func skipNewsletterRule() *domain.Rule {
	return &domain.Rule{
		Name:       "skip newsletters",
		Priority:   0,
		Conditions: domain.RuleNode{Type: domain.ConditionSubjectContains, Value: "(?i)newsletter"},
		Action:     domain.ActionSkip,
		IsActive:   true,
	}
}

// TestFetchNewMessages_SkipRule_DoesNotArchiveButAdvancesLastUID covers
// FR-RE-03's skip action end to end: a matching message must not produce
// an emails row, a Maildir file, or an index entry, but folders.last_uid
// still has to move past it — otherwise every future sync would refetch
// and re-evaluate the same skipped message forever.
func TestFetchNewMessages_SkipRule_DoesNotArchiveButAdvancesLastUID(t *testing.T) {
	env := newFetchTestEnv(t)

	addr := startFakeFetchServer(t, fakeFetchServer{
		uidValidity: 1001,
		uidNext:     3,
		messages: []fakeMessage{
			{uid: 1, flags: "", body: testEmail("Weekly newsletter")},
			{uid: 2, flags: "", body: testEmail("Important invoice")},
		},
	})
	c := connectToFakeServer(t, addr)
	defer c.Logout()

	folder, err := env.foldersR.UpsertFolder(context.Background(), env.accountID, "INBOX", 1001)
	if err != nil {
		t.Fatalf("UpsertFolder: %v", err)
	}
	mw := env.newWriter(t, "INBOX")

	activeRules := []*domain.Rule{skipNewsletterRule()}
	stats, err := FetchNewMessages(context.Background(), c, env.accountID, folder, mw, env.w, env.emailsR, env.foldersR, env.attachmentsR, nil, activeRules, nil)
	if err != nil {
		t.Fatalf("FetchNewMessages: %v", err)
	}
	if stats.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1", stats.Skipped)
	}
	if stats.Archived != 1 {
		t.Errorf("Archived = %d, want 1", stats.Archived)
	}
	if stats.Processed != 2 {
		t.Errorf("Processed = %d, want 2", stats.Processed)
	}
	if stats.Errors != 0 {
		t.Errorf("Errors = %d, want 0", stats.Errors)
	}

	emails, err := env.emailsR.ListByAccount(context.Background(), env.accountID)
	if err != nil {
		t.Fatalf("ListByAccount: %v", err)
	}
	if len(emails) != 1 {
		t.Fatalf("archived %d emails, want 1 (the newsletter must not be archived)", len(emails))
	}
	if emails[0].Subject != "Important invoice" {
		t.Errorf("archived email = %q, want the non-skipped message", emails[0].Subject)
	}

	updated, err := env.foldersR.ListByAccount(context.Background(), env.accountID)
	if err != nil {
		t.Fatalf("ListByAccount(folders): %v", err)
	}
	if len(updated) != 1 || updated[0].LastUID != 2 {
		t.Fatalf("folder LastUID = %+v, want last_uid=2 (advanced past the skipped UID 1 too)", updated)
	}
}

// TestFetchNewMessages_NoMatchingRule_DefaultsToArchive covers the
// backward-compatibility guarantee: a Candidate that matches no active
// rule archives exactly like Phase 1/2 did with no Rule Engine at all.
func TestFetchNewMessages_NoMatchingRule_DefaultsToArchive(t *testing.T) {
	env := newFetchTestEnv(t)

	addr := startFakeFetchServer(t, fakeFetchServer{
		uidValidity: 1001,
		uidNext:     2,
		messages: []fakeMessage{
			{uid: 1, flags: "", body: testEmail("Nothing special")},
		},
	})
	c := connectToFakeServer(t, addr)
	defer c.Logout()

	folder, err := env.foldersR.UpsertFolder(context.Background(), env.accountID, "INBOX", 1001)
	if err != nil {
		t.Fatalf("UpsertFolder: %v", err)
	}
	mw := env.newWriter(t, "INBOX")

	activeRules := []*domain.Rule{skipNewsletterRule()} // won't match "Nothing special"
	stats, err := FetchNewMessages(context.Background(), c, env.accountID, folder, mw, env.w, env.emailsR, env.foldersR, env.attachmentsR, nil, activeRules, nil)
	if err != nil {
		t.Fatalf("FetchNewMessages: %v", err)
	}
	if stats.Archived != 1 || stats.Skipped != 0 {
		t.Errorf("stats = %+v, want Archived=1 Skipped=0", stats)
	}
}
