package search

import (
	"context"
	"testing"
	"time"
)

func seedForSearch(t *testing.T, idx *Index) {
	t.Helper()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	docs := []Doc{
		{
			EmailID: 1, MessageID: "1@x", Subject: "Quarterly report",
			From: `"Alice" <alice@example.com>`, FromAddr: "alice@example.com",
			To: []string{`"Bob" <bob@example.com>`}, ToAddrs: []string{"bob@example.com"},
			Body: "numbers attached", Date: base, AccountID: 1, FolderID: 10,
			HasAttachments: true, Size: 100,
		},
		{
			EmailID: 2, MessageID: "2@x", Subject: "Lunch tomorrow",
			From: `"Carol" <carol@example.com>`, FromAddr: "carol@example.com",
			To: []string{`"Bob" <bob@example.com>`}, ToAddrs: []string{"bob@example.com"},
			Body: "tacos at noon", Date: base.AddDate(0, 0, 1), AccountID: 1, FolderID: 20,
			HasAttachments: false, Size: 50,
		},
		{
			EmailID: 3, MessageID: "3@x", Subject: "Old newsletter",
			From: `"Dave" <dave@example.com>`, FromAddr: "dave@example.com",
			Cc: []string{`"Bob" <bob@example.com>`}, CcAddrs: []string{"bob@example.com"},
			Body: "quarterly digest", Date: base.AddDate(-1, 0, 0), AccountID: 2, FolderID: 30,
			HasAttachments: false, Size: 20,
		},
	}
	for _, d := range docs {
		if err := idx.Index(d); err != nil {
			t.Fatalf("seeding doc %d: %v", d.EmailID, err)
		}
	}
}

func emailIDs(res Result) []int64 {
	ids := make([]int64, len(res.Hits))
	for i, h := range res.Hits {
		ids[i] = h.EmailID
	}
	return ids
}

func TestSearch_FreeTextMatchesAcrossFields(t *testing.T) {
	idx := openTestIndex(t)
	seedForSearch(t, idx)

	res, err := idx.Search(context.Background(), Params{Query: "quarterly"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if res.Total != 2 {
		t.Errorf("Total = %d, want 2 (subject match on 1, body match on 3)", res.Total)
	}
}

func TestSearch_NoQuery_MatchesEverything(t *testing.T) {
	idx := openTestIndex(t)
	seedForSearch(t, idx)

	res, err := idx.Search(context.Background(), Params{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if res.Total != 3 {
		t.Errorf("Total = %d, want 3 (empty query = browse everything)", res.Total)
	}
}

func TestSearch_FilterByAccountAndFolder(t *testing.T) {
	idx := openTestIndex(t)
	seedForSearch(t, idx)

	res, err := idx.Search(context.Background(), Params{AccountID: 1})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if res.Total != 2 {
		t.Errorf("account_id=1: Total = %d, want 2", res.Total)
	}

	res, err = idx.Search(context.Background(), Params{FolderID: 20})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if res.Total != 1 || len(res.Hits) != 1 || res.Hits[0].EmailID != 2 {
		t.Errorf("folder_id=20: got %+v, want exactly email 2", res)
	}
}

func TestSearch_FilterByHasAttachments(t *testing.T) {
	idx := openTestIndex(t)
	seedForSearch(t, idx)

	yes := true
	res, err := idx.Search(context.Background(), Params{HasAttachments: &yes})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if res.Total != 1 || res.Hits[0].EmailID != 1 {
		t.Errorf("has_attachments=true: got %+v, want exactly email 1", res)
	}

	no := false
	res, err = idx.Search(context.Background(), Params{HasAttachments: &no})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if res.Total != 2 {
		t.Errorf("has_attachments=false: Total = %d, want 2", res.Total)
	}
}

func TestSearch_FilterBySenderExact(t *testing.T) {
	idx := openTestIndex(t)
	seedForSearch(t, idx)

	res, err := idx.Search(context.Background(), Params{Sender: "alice@example.com"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if res.Total != 1 || res.Hits[0].EmailID != 1 {
		t.Errorf("sender filter: got %+v, want exactly email 1", res)
	}
}

func TestSearch_FilterByRecipient_MatchesToOrCc(t *testing.T) {
	idx := openTestIndex(t)
	seedForSearch(t, idx)

	res, err := idx.Search(context.Background(), Params{Recipient: "bob@example.com"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	// bob is a To on docs 1 and 2, and a Cc on doc 3 — all three should match.
	if res.Total != 3 {
		t.Errorf("recipient filter: Total = %d, want 3 (To on 1/2, Cc on 3)", res.Total)
	}
}

func TestSearch_DateRangeFilter(t *testing.T) {
	idx := openTestIndex(t)
	seedForSearch(t, idx)

	res, err := idx.Search(context.Background(), Params{
		DateFrom: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		DateTo:   time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	// doc 3 is a year before the range and must be excluded.
	if res.Total != 2 {
		t.Errorf("date range: Total = %d, want 2 (excluding the year-old doc 3)", res.Total)
	}
	for _, h := range res.Hits {
		if h.EmailID == 3 {
			t.Error("doc 3 (outside the date range) matched, want it excluded")
		}
	}
}

func TestSearch_SortByDate(t *testing.T) {
	idx := openTestIndex(t)
	seedForSearch(t, idx)

	res, err := idx.Search(context.Background(), Params{Sort: SortDateAsc})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got := emailIDs(res); len(got) != 3 || got[0] != 3 || got[2] != 2 {
		t.Errorf("SortDateAsc order = %v, want [3 1 2] (oldest first)", got)
	}

	res, err = idx.Search(context.Background(), Params{Sort: SortDateDesc})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got := emailIDs(res); len(got) != 3 || got[0] != 2 || got[2] != 3 {
		t.Errorf("SortDateDesc order = %v, want [2 1 3] (newest first)", got)
	}
}

func TestSearch_PaginationOffsetLimit(t *testing.T) {
	idx := openTestIndex(t)
	seedForSearch(t, idx)

	res, err := idx.Search(context.Background(), Params{Sort: SortDateAsc, Limit: 1})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if res.Total != 3 {
		t.Errorf("Total = %d, want 3 (unaffected by limit)", res.Total)
	}
	if len(res.Hits) != 1 || res.Hits[0].EmailID != 3 {
		t.Errorf("page 1: got %v, want exactly email 3", emailIDs(res))
	}

	res, err = idx.Search(context.Background(), Params{Sort: SortDateAsc, Limit: 1, Offset: 1})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res.Hits) != 1 || res.Hits[0].EmailID != 1 {
		t.Errorf("page 2: got %v, want exactly email 1", emailIDs(res))
	}
}

func TestSearch_LimitDefaultsAndCaps(t *testing.T) {
	idx := openTestIndex(t)
	seedForSearch(t, idx)

	res, err := idx.Search(context.Background(), Params{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if res.Limit != DefaultLimit {
		t.Errorf("Limit = %d, want DefaultLimit (%d)", res.Limit, DefaultLimit)
	}

	res, err = idx.Search(context.Background(), Params{Limit: MaxLimit + 1000})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if res.Limit != MaxLimit {
		t.Errorf("Limit = %d, want it capped to MaxLimit (%d)", res.Limit, MaxLimit)
	}
}

func TestSearch_HitFieldsPopulated(t *testing.T) {
	idx := openTestIndex(t)
	seedForSearch(t, idx)

	res, err := idx.Search(context.Background(), Params{Query: "quarterly report"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	var hit *Hit
	for i := range res.Hits {
		if res.Hits[i].EmailID == 1 {
			hit = &res.Hits[i]
		}
	}
	if hit == nil {
		t.Fatal("email 1 not found in results")
	}
	if hit.Subject != "Quarterly report" {
		t.Errorf("Subject = %q", hit.Subject)
	}
	if hit.From != `"Alice" <alice@example.com>` {
		t.Errorf("From = %q", hit.From)
	}
	if hit.AccountID != 1 || hit.FolderID != 10 {
		t.Errorf("AccountID/FolderID = %d/%d, want 1/10", hit.AccountID, hit.FolderID)
	}
	if !hit.HasAttachments {
		t.Error("HasAttachments = false, want true")
	}
	if hit.Size != 100 {
		t.Errorf("Size = %d, want 100", hit.Size)
	}
	if hit.Score <= 0 {
		t.Errorf("Score = %v, want > 0", hit.Score)
	}
}
