package search

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/blugelabs/bluge"
	bsearch "github.com/blugelabs/bluge/search"
)

// DefaultLimit and MaxLimit are FR-SR-03's pagination bounds.
const (
	DefaultLimit = 50
	MaxLimit     = 500
)

// SortOrder is FR-SR-03's "Сортировка: по релевантности (default), по
// дате (asc/desc)".
type SortOrder int

const (
	SortRelevance SortOrder = iota
	SortDateAsc
	SortDateDesc
)

// Params is one search request (FR-SR-03). The zero value of every
// optional field means "no filter": Query == "" matches every document,
// DateFrom/DateTo zero are unbounded, AccountID/FolderID == 0 means
// unfiltered (valid ids are autoincrement starting at 1), and
// HasAttachments == nil means unfiltered — a plain bool can't distinguish
// "filter for false" from "don't filter", hence the pointer.
type Params struct {
	Query          string
	DateFrom       time.Time
	DateTo         time.Time
	Sender         string // bare address, matched against FieldFromExact
	Recipient      string // bare address, matched against FieldToExact OR FieldCcExact
	AccountID      int64
	FolderID       int64
	HasAttachments *bool
	Sort           SortOrder
	Offset         int
	Limit          int
}

// Hit is one search result — enough to render a results list; the full
// email (and its .eml/attachments) is fetched separately by id once a
// result is opened.
type Hit struct {
	EmailID        int64
	MessageID      string
	Subject        string
	From           string
	Date           time.Time
	AccountID      int64
	FolderID       int64
	HasAttachments bool
	Size           int64
	Score          float64
}

// Result is one page of search results.
type Result struct {
	Total  uint64
	Offset int
	Limit  int
	Hits   []Hit
}

// Search runs p against the index (FR-SR-03: free-text query across every
// indexed field via the composite "_all" field, combined with structured
// filters, sorting, and offset/limit pagination).
func (idx *Index) Search(ctx context.Context, p Params) (Result, error) {
	limit := p.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}
	offset := p.Offset
	if offset < 0 {
		offset = 0
	}

	req := bluge.NewTopNSearch(limit, buildQuery(p)).SetFrom(offset).WithStandardAggregations()
	switch p.Sort {
	case SortDateAsc:
		req = req.SortBy([]string{FieldDate})
	case SortDateDesc:
		req = req.SortBy([]string{"-" + FieldDate})
	case SortRelevance:
		// Bluge's default order (score descending) is already relevance.
	}

	reader, err := idx.Reader()
	if err != nil {
		return Result{}, fmt.Errorf("search: opening reader: %w", err)
	}
	defer reader.Close()

	iter, err := reader.Search(ctx, req)
	if err != nil {
		return Result{}, fmt.Errorf("search: executing query: %w", err)
	}

	res := Result{Offset: offset, Limit: limit}
	for {
		match, err := iter.Next()
		if err != nil {
			return Result{}, fmt.Errorf("search: iterating results: %w", err)
		}
		if match == nil {
			break
		}
		res.Hits = append(res.Hits, hitFromMatch(match))
	}

	if agg := iter.Aggregations(); agg != nil {
		res.Total = agg.Count()
	}
	return res, nil
}

func hitFromMatch(match *bsearch.DocumentMatch) Hit {
	h := Hit{Score: match.Score}
	_ = match.VisitStoredFields(func(field string, value []byte) bool {
		switch field {
		case FieldEmailID:
			if n, err := bluge.DecodeNumericFloat64(value); err == nil {
				h.EmailID = int64(n)
			}
		case FieldMessageID:
			h.MessageID = string(value)
		case FieldSubject:
			h.Subject = string(value)
		case FieldFrom:
			h.From = string(value)
		case FieldDate:
			if t, err := bluge.DecodeDateTime(value); err == nil {
				h.Date = t
			}
		case FieldAccountID:
			h.AccountID, _ = strconv.ParseInt(string(value), 10, 64)
		case FieldFolderID:
			h.FolderID, _ = strconv.ParseInt(string(value), 10, 64)
		case FieldHasAttachments:
			h.HasAttachments = string(value) == "t"
		case FieldSize:
			if n, err := bluge.DecodeNumericFloat64(value); err == nil {
				h.Size = int64(n)
			}
		}
		return true
	})
	return h
}

// buildQuery translates Params into a Bluge query: a free-text match
// against the composite "_all" field (or match-everything if Query is
// empty, so pure filter browsing works with no search term at all),
// combined with every active structured filter via AND (BooleanQuery's
// Must clauses).
func buildQuery(p Params) bluge.Query {
	bq := bluge.NewBooleanQuery()

	if p.Query != "" {
		bq.AddMust(bluge.NewMatchQuery(p.Query).SetField(FieldAll).SetFuzziness(1))
	} else {
		bq.AddMust(bluge.NewMatchAllQuery())
	}

	if !p.DateFrom.IsZero() || !p.DateTo.IsZero() {
		from := p.DateFrom
		if from.IsZero() {
			from = time.Unix(0, 0).UTC()
		}
		to := p.DateTo
		if to.IsZero() {
			to = time.Now().AddDate(100, 0, 0)
		}
		bq.AddMust(bluge.NewDateRangeInclusiveQuery(from, to, true, true).SetField(FieldDate))
	}
	if p.Sender != "" {
		bq.AddMust(bluge.NewTermQuery(p.Sender).SetField(FieldFromExact))
	}
	if p.Recipient != "" {
		// "recipient" means either To or Cc — a disjunction (Should, with
		// a minimum of 1) inside the overall AND of filters.
		recipientQ := bluge.NewBooleanQuery().
			AddShould(bluge.NewTermQuery(p.Recipient).SetField(FieldToExact)).
			AddShould(bluge.NewTermQuery(p.Recipient).SetField(FieldCcExact))
		recipientQ.SetMinShould(1)
		bq.AddMust(recipientQ)
	}
	if p.AccountID != 0 {
		bq.AddMust(bluge.NewTermQuery(strconv.FormatInt(p.AccountID, 10)).SetField(FieldAccountID))
	}
	if p.FolderID != 0 {
		bq.AddMust(bluge.NewTermQuery(strconv.FormatInt(p.FolderID, 10)).SetField(FieldFolderID))
	}
	if p.HasAttachments != nil {
		bq.AddMust(bluge.NewTermQuery(BoolTerm(*p.HasAttachments)).SetField(FieldHasAttachments))
	}

	return bq
}
