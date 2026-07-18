// Package search wraps Bluge, MailVault's full-text search engine
// (FR-SR-01..04). The index lives at {data_dir}/index/ and is never
// replicated to S3 (FR-SR-01) — losing it just means a reindex (FR-SR-04,
// see the reindex step) rebuilding it from the local .eml files that are
// always the actual source of truth.
package search

import (
	"fmt"
	"strconv"
	"time"

	"github.com/blugelabs/bluge"
)

// Field names match FR-SR-02's mapping. From/To/Cc are indexed twice, once
// analyzed (fuzzy/substring-ish free-text matching) and once as an exact
// keyword under a "_exact" suffix — Bluge fields are looked up by name, so
// mixing an analyzed and unanalyzed value under the very same field name
// would blend two incompatible term types together; separate field names
// keep "search for gmail.com in From" and "filter From = exact address"
// unambiguous for the search API (a later step) to query independently.
const (
	FieldMessageID       = "message_id"
	FieldSubject         = "subject"
	FieldFrom            = "from"
	FieldFromExact       = "from_exact"
	FieldTo              = "to"
	FieldToExact         = "to_exact"
	FieldCc              = "cc"
	FieldCcExact         = "cc_exact"
	FieldBody            = "body"
	FieldAttachmentNames = "attachment_names"
	FieldDate            = "date"
	FieldAccountID       = "account_id"
	FieldFolderID        = "folder_id"
	FieldHasAttachments  = "has_attachments"
	FieldSize            = "size"

	// FieldAll is a composite catch-all field (FR-SR-03's "простой
	// текстовый поиск по всем проиндексированным полям") aggregating
	// every analyzed text field, so a single free-text query can search
	// across all of them at once.
	FieldAll = "_all"
)

// Doc is one email's worth of indexable content. From/To/Cc are the
// display-formatted mailbox strings (analyzed text search);
// FromAddr/ToAddrs/CcAddrs are the bare email addresses used for the
// exact-match fields (FieldFromExact etc.) — deliberately separate so
// filtering by "alice@example.com" doesn't depend on matching a display
// name or RFC 5322 angle brackets too.
type Doc struct {
	EmailID         int64
	MessageID       string
	Subject         string
	From            string
	FromAddr        string
	To              []string
	ToAddrs         []string
	Cc              []string
	CcAddrs         []string
	Body            string
	AttachmentNames []string
	Date            time.Time
	AccountID       int64
	FolderID        int64
	HasAttachments  bool
	Size            int64
}

// Index wraps a Bluge index writer.
type Index struct {
	writer *bluge.Writer
}

// Open opens the Bluge index at path, creating it on first use.
func Open(path string) (*Index, error) {
	w, err := bluge.OpenWriter(bluge.DefaultConfig(path))
	if err != nil {
		return nil, fmt.Errorf("search: opening index at %s: %w", path, err)
	}
	return &Index{writer: w}, nil
}

// Close releases the index's file handles. Safe to call once.
func (idx *Index) Close() error {
	return idx.writer.Close()
}

// Reader opens a read-only snapshot of the index's current state, for
// querying (used by tests here, and by the search API added in a later
// step).
func (idx *Index) Reader() (*bluge.Reader, error) {
	return idx.writer.Reader()
}

// writeTimeout bounds every call into the underlying Bluge writer. This
// exists because a Bluge write doesn't reliably return an error under all
// failure conditions — calling Batch() on a writer that's already been
// Close()d, for instance, blocks forever on an internal channel send
// instead of erroring out (observed directly while testing this
// package's best-effort contract with internal/sync). Every caller of
// Index/Delete treats a broken search index as non-fatal to archival
// (FR-SR-01/02); a bound here is what actually makes that true in
// practice, rather than just in the happy path.
const writeTimeout = 5 * time.Second

// Index adds or replaces the document for one email — an upsert (Bluge's
// Writer.Update), which matters both for normal re-syncs of an already
// archived message and for a full reindex (FR-SR-04).
func (idx *Index) Index(d Doc) error {
	doc := buildDocument(d)
	return idx.withTimeout(func() error {
		return idx.writer.Update(doc.ID(), *doc)
	})
}

// Delete removes emailID's document, if any (FR-AM-06 cascade delete).
func (idx *Index) Delete(emailID int64) error {
	return idx.withTimeout(func() error {
		return idx.writer.Delete(bluge.Identifier(emailIDTerm(emailID)))
	})
}

// withTimeout runs fn in its own goroutine and waits up to writeTimeout for
// it to finish. If fn never returns, this leaks that goroutine — an
// accepted trade-off (see writeTimeout's doc comment): a wedged Bluge
// writer is an abnormal, rare condition, and leaking one goroutine on it
// is strictly better than the caller (the sync engine) hanging forever.
func (idx *Index) withTimeout(fn func() error) error {
	done := make(chan error, 1)
	go func() { done <- fn() }()
	select {
	case err := <-done:
		return err
	case <-time.After(writeTimeout):
		return fmt.Errorf("search: index write timed out after %s", writeTimeout)
	}
}

func buildDocument(d Doc) *bluge.Document {
	doc := bluge.NewDocument(emailIDTerm(d.EmailID))

	doc.AddField(bluge.NewKeywordField(FieldMessageID, d.MessageID).StoreValue())
	doc.AddField(bluge.NewTextField(FieldSubject, d.Subject).StoreValue().SearchTermPositions().HighlightMatches())

	doc.AddField(bluge.NewTextField(FieldFrom, d.From))
	doc.AddField(bluge.NewKeywordField(FieldFromExact, orFallback(d.FromAddr, d.From)).StoreValue())

	for i, to := range d.To {
		doc.AddField(bluge.NewTextField(FieldTo, to))
		exact := to
		if i < len(d.ToAddrs) {
			exact = d.ToAddrs[i]
		}
		doc.AddField(bluge.NewKeywordField(FieldToExact, exact).StoreValue())
	}
	for i, cc := range d.Cc {
		doc.AddField(bluge.NewTextField(FieldCc, cc))
		exact := cc
		if i < len(d.CcAddrs) {
			exact = d.CcAddrs[i]
		}
		doc.AddField(bluge.NewKeywordField(FieldCcExact, exact).StoreValue())
	}

	doc.AddField(bluge.NewTextField(FieldBody, d.Body).SearchTermPositions().HighlightMatches())

	for _, name := range d.AttachmentNames {
		doc.AddField(bluge.NewKeywordField(FieldAttachmentNames, name).StoreValue())
	}

	doc.AddField(bluge.NewDateTimeField(FieldDate, d.Date).StoreValue().Sortable())
	doc.AddField(bluge.NewKeywordField(FieldAccountID, strconv.FormatInt(d.AccountID, 10)).StoreValue())
	doc.AddField(bluge.NewKeywordField(FieldFolderID, strconv.FormatInt(d.FolderID, 10)).StoreValue())
	doc.AddField(bluge.NewKeywordField(FieldHasAttachments, boolTerm(d.HasAttachments)).StoreValue())
	doc.AddField(bluge.NewNumericField(FieldSize, float64(d.Size)).StoreValue().Sortable())

	doc.AddField(bluge.NewCompositeFieldIncluding(FieldAll,
		[]string{FieldSubject, FieldFrom, FieldTo, FieldCc, FieldBody, FieldAttachmentNames, FieldMessageID}))

	return doc
}

func emailIDTerm(id int64) string {
	return strconv.FormatInt(id, 10)
}

// orFallback returns addr if it's set, otherwise display — a caller that
// only populates the display-formatted From/To/Cc (not the *Addr/*Addrs
// bare-address counterparts) still gets a usable, if imperfect, exact
// field rather than an empty one.
func orFallback(addr, display string) string {
	if addr != "" {
		return addr
	}
	return display
}

// boolTerm is Bluge's own convention for indexing a boolean as a keyword
// field: "t"/"f", not "true"/"false".
func boolTerm(b bool) string {
	if b {
		return "t"
	}
	return "f"
}
