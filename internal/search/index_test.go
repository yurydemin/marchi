package search

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/blugelabs/bluge"
)

func openTestIndex(t *testing.T) *Index {
	t.Helper()
	idx, err := Open(filepath.Join(t.TempDir(), "index"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { idx.Close() })
	return idx
}

// searchMessageIDs runs query against idx's current state and returns the
// message_id stored field of every match — good enough to assert on
// without needing this step's later search API.
func searchMessageIDs(t *testing.T, idx *Index, query bluge.Query) []string {
	t.Helper()
	reader, err := idx.Reader()
	if err != nil {
		t.Fatalf("Reader: %v", err)
	}
	defer reader.Close()

	iter, err := reader.Search(context.Background(), bluge.NewTopNSearch(10, query))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	var ids []string
	for {
		match, err := iter.Next()
		if err != nil {
			t.Fatalf("iterating results: %v", err)
		}
		if match == nil {
			break
		}
		var messageID string
		_ = match.VisitStoredFields(func(field string, value []byte) bool {
			if field == FieldMessageID {
				messageID = string(value)
			}
			return true
		})
		ids = append(ids, messageID)
	}
	return ids
}

func TestIndex_IndexAndSearch_FreeTextViaCompositeField(t *testing.T) {
	idx := openTestIndex(t)

	if err := idx.Index(Doc{
		EmailID:   1,
		MessageID: "msg-1@example.com",
		Subject:   "Quarterly report attached",
		From:      "alice@example.com",
		Body:      "Please find the numbers below.",
		Date:      time.Now(),
	}); err != nil {
		t.Fatalf("Index: %v", err)
	}
	if err := idx.Index(Doc{
		EmailID:   2,
		MessageID: "msg-2@example.com",
		Subject:   "Lunch tomorrow?",
		From:      "bob@example.com",
		Body:      "Are you free at noon?",
		Date:      time.Now(),
	}); err != nil {
		t.Fatalf("Index: %v", err)
	}

	query := bluge.NewMatchQuery("quarterly").SetField(FieldAll)
	ids := searchMessageIDs(t, idx, query)
	if len(ids) != 1 || ids[0] != "msg-1@example.com" {
		t.Errorf("got %v, want exactly [msg-1@example.com]", ids)
	}
}

func TestIndex_ExactKeywordField_FromExact(t *testing.T) {
	idx := openTestIndex(t)

	if err := idx.Index(Doc{
		EmailID:   1,
		MessageID: "msg-1@example.com",
		From:      "alice@example.com",
		Date:      time.Now(),
	}); err != nil {
		t.Fatalf("Index: %v", err)
	}

	// A substring of the address should NOT match the exact keyword field
	// — that's the whole point of keeping it separate from the analyzed
	// "from" field.
	query := bluge.NewTermQuery("alice").SetField(FieldFromExact)
	if ids := searchMessageIDs(t, idx, query); len(ids) != 0 {
		t.Errorf("substring query against the exact field matched %v, want none", ids)
	}

	query = bluge.NewTermQuery("alice@example.com").SetField(FieldFromExact)
	if ids := searchMessageIDs(t, idx, query); len(ids) != 1 {
		t.Errorf("exact query got %v, want exactly one match", ids)
	}
}

// TestIndex_ExactField_UsesBareAddressOverDisplayFormat covers a real bug
// caught via a live demo against a real Dovecot mailbox: mimeparse's
// From/To/Cc are RFC 5322 mailbox strings (e.g. "<a@x.com>" with no
// display name, or `"Name" <a@x.com>` with one) — indexing those directly
// under *_exact would make "filter by a@x.com" never match anything. The
// dedicated *Addr/*Addrs fields must be what actually lands in the exact
// fields.
func TestIndex_ExactField_UsesBareAddressOverDisplayFormat(t *testing.T) {
	idx := openTestIndex(t)

	if err := idx.Index(Doc{
		EmailID:   1,
		MessageID: "msg-1@example.com",
		From:      `"Alice" <alice@example.com>`, // display-formatted, as mimeparse.Metadata.From would be
		FromAddr:  "alice@example.com",
		To:        []string{`"Bob" <bob@example.com>`},
		ToAddrs:   []string{"bob@example.com"},
		Date:      time.Now(),
	}); err != nil {
		t.Fatalf("Index: %v", err)
	}

	if ids := searchMessageIDs(t, idx, bluge.NewTermQuery("alice@example.com").SetField(FieldFromExact)); len(ids) != 1 {
		t.Errorf("exact query for the bare From address got %v, want exactly one match", ids)
	}
	if ids := searchMessageIDs(t, idx, bluge.NewTermQuery(`"Alice" <alice@example.com>`).SetField(FieldFromExact)); len(ids) != 0 {
		t.Errorf("exact query for the display-formatted From string matched %v, want none", ids)
	}
	if ids := searchMessageIDs(t, idx, bluge.NewTermQuery("bob@example.com").SetField(FieldToExact)); len(ids) != 1 {
		t.Errorf("exact query for the bare To address got %v, want exactly one match", ids)
	}
}

func TestIndex_Delete_RemovesDocument(t *testing.T) {
	idx := openTestIndex(t)

	if err := idx.Index(Doc{EmailID: 1, MessageID: "msg-1@example.com", Subject: "keepme", Date: time.Now()}); err != nil {
		t.Fatalf("Index: %v", err)
	}
	if err := idx.Index(Doc{EmailID: 2, MessageID: "msg-2@example.com", Subject: "deleteme", Date: time.Now()}); err != nil {
		t.Fatalf("Index: %v", err)
	}

	if err := idx.Delete(2); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	ids := searchMessageIDs(t, idx, bluge.NewMatchQuery("keepme").SetField(FieldAll))
	if len(ids) != 1 || ids[0] != "msg-1@example.com" {
		t.Errorf("got %v after deleting doc 2, want exactly [msg-1@example.com]", ids)
	}
	ids = searchMessageIDs(t, idx, bluge.NewMatchQuery("deleteme").SetField(FieldAll))
	if len(ids) != 0 {
		t.Errorf("deleted document still matched: %v", ids)
	}
}

func TestIndex_Update_UpsertsRatherThanDuplicates(t *testing.T) {
	idx := openTestIndex(t)

	if err := idx.Index(Doc{EmailID: 1, MessageID: "msg-1@example.com", Subject: "original subject", Date: time.Now()}); err != nil {
		t.Fatalf("Index: %v", err)
	}
	if err := idx.Index(Doc{EmailID: 1, MessageID: "msg-1@example.com", Subject: "revised subject", Date: time.Now()}); err != nil {
		t.Fatalf("Index: %v", err)
	}

	if ids := searchMessageIDs(t, idx, bluge.NewMatchQuery("original").SetField(FieldAll)); len(ids) != 0 {
		t.Errorf("stale version of the document still matched: %v", ids)
	}
	ids := searchMessageIDs(t, idx, bluge.NewMatchQuery("revised").SetField(FieldAll))
	if len(ids) != 1 {
		t.Errorf("got %v, want exactly one match for the updated document", ids)
	}
}
