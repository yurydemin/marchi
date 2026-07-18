package sync

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/blugelabs/bluge"

	"github.com/yurydemin/marchi/internal/search"
)

func openTestIndex(t *testing.T) *search.Index {
	t.Helper()
	idx, err := search.Open(filepath.Join(t.TempDir(), "index"))
	if err != nil {
		t.Fatalf("search.Open: %v", err)
	}
	t.Cleanup(func() { idx.Close() })
	return idx
}

func countBodyMatches(t *testing.T, idx *search.Index, term string) int {
	t.Helper()
	reader, err := idx.Reader()
	if err != nil {
		t.Fatalf("Reader: %v", err)
	}
	defer reader.Close()

	query := bluge.NewMatchQuery(term).SetField(search.FieldBody)
	iter, err := reader.Search(context.Background(), bluge.NewTopNSearch(10, query))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	n := 0
	for {
		match, err := iter.Next()
		if err != nil {
			t.Fatalf("iterating results: %v", err)
		}
		if match == nil {
			break
		}
		n++
	}
	return n
}

// TestFetchNewMessages_IndexesArchivedMessages exercises the best-effort
// search-index integration end to end: a real *search.Index is threaded
// through FetchNewMessages -> archiveOne, and the archived message's body
// text should actually be searchable afterward (FR-SR-01/02).
func TestFetchNewMessages_IndexesArchivedMessages(t *testing.T) {
	env := newFetchTestEnv(t)
	idx := openTestIndex(t)

	addr := startFakeFetchServer(t, fakeFetchServer{
		uidValidity: 1001,
		uidNext:     2,
		messages: []fakeMessage{
			{uid: 1, flags: "", body: testEmail("uniquesubjectterm")},
		},
	})
	c := connectToFakeServer(t, addr)
	defer c.Logout()

	folder, err := env.foldersR.UpsertFolder(context.Background(), env.accountID, "INBOX", 1001)
	if err != nil {
		t.Fatalf("UpsertFolder: %v", err)
	}
	mw := env.newWriter(t, "INBOX")

	stats, err := FetchNewMessages(context.Background(), c, env.accountID, folder, mw, env.w, env.emailsR, env.foldersR, env.attachmentsR, idx, nil, nil)
	if err != nil {
		t.Fatalf("FetchNewMessages: %v", err)
	}
	if stats.Archived != 1 {
		t.Fatalf("Archived = %d, want 1", stats.Archived)
	}
	if stats.IndexErrors != 0 {
		t.Errorf("IndexErrors = %d, want 0", stats.IndexErrors)
	}

	// testEmail's body is "Body of <subject>\r\n" — the subject itself
	// ("uniquesubjectterm") appears in the body text too, so searching the
	// body field for it proves ParseBody's output actually made it into
	// the index, not just the subject field.
	if n := countBodyMatches(t, idx, "uniquesubjectterm"); n != 1 {
		t.Errorf("search for the archived message's body term returned %d hits, want 1", n)
	}
}

// TestFetchNewMessages_NilIndex_SkipsIndexingWithoutError confirms the
// nil-idx path (used by callers that don't care about search, and by
// every other test in this package) truly is a no-op, not a silent
// failure that happens to look the same.
func TestFetchNewMessages_NilIndex_SkipsIndexingWithoutError(t *testing.T) {
	env := newFetchTestEnv(t)

	addr := startFakeFetchServer(t, fakeFetchServer{
		uidValidity: 1001,
		uidNext:     2,
		messages:    []fakeMessage{{uid: 1, flags: "", body: testEmail("whatever")}},
	})
	c := connectToFakeServer(t, addr)
	defer c.Logout()

	folder, err := env.foldersR.UpsertFolder(context.Background(), env.accountID, "INBOX", 1001)
	if err != nil {
		t.Fatalf("UpsertFolder: %v", err)
	}
	mw := env.newWriter(t, "INBOX")

	stats, err := FetchNewMessages(context.Background(), c, env.accountID, folder, mw, env.w, env.emailsR, env.foldersR, env.attachmentsR, nil, nil, nil)
	if err != nil {
		t.Fatalf("FetchNewMessages: %v", err)
	}
	if stats.Archived != 1 {
		t.Fatalf("Archived = %d, want 1", stats.Archived)
	}
	if stats.IndexErrors != 0 {
		t.Errorf("IndexErrors = %d, want 0 (nil index should never count as a failure)", stats.IndexErrors)
	}
}

// TestFetchNewMessages_IndexWriteFails_ArchivalStillSucceeds is the core
// best-effort guarantee (see archiveOne's doc comment): a broken search
// index must never block or fail archival. Simulated here by handing in
// an already-closed *search.Index, so every Index() call inside
// archiveOne fails.
func TestFetchNewMessages_IndexWriteFails_ArchivalStillSucceeds(t *testing.T) {
	env := newFetchTestEnv(t)
	idx, err := search.Open(filepath.Join(t.TempDir(), "index"))
	if err != nil {
		t.Fatalf("search.Open: %v", err)
	}
	if err := idx.Close(); err != nil {
		t.Fatalf("closing index up front: %v", err)
	}

	addr := startFakeFetchServer(t, fakeFetchServer{
		uidValidity: 1001,
		uidNext:     2,
		messages:    []fakeMessage{{uid: 1, flags: "", body: testEmail("whatever")}},
	})
	c := connectToFakeServer(t, addr)
	defer c.Logout()

	folder, err := env.foldersR.UpsertFolder(context.Background(), env.accountID, "INBOX", 1001)
	if err != nil {
		t.Fatalf("UpsertFolder: %v", err)
	}
	mw := env.newWriter(t, "INBOX")

	stats, err := FetchNewMessages(context.Background(), c, env.accountID, folder, mw, env.w, env.emailsR, env.foldersR, env.attachmentsR, idx, nil, nil)
	if err != nil {
		t.Fatalf("FetchNewMessages returned an error: %v (archival must succeed even though indexing fails)", err)
	}
	if stats.Archived != 1 {
		t.Fatalf("Archived = %d, want 1 (a broken index must not block archival)", stats.Archived)
	}
	if stats.IndexErrors != 1 {
		t.Errorf("IndexErrors = %d, want 1", stats.IndexErrors)
	}

	emails, err := env.emailsR.ListByFolder(context.Background(), folder.ID)
	if err != nil {
		t.Fatalf("ListByFolder: %v", err)
	}
	if len(emails) != 1 {
		t.Fatalf("got %d emails in DB, want 1 — the row must exist regardless of the indexing failure", len(emails))
	}
}
