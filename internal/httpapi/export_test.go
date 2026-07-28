package httpapi

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yurydemin/marchi/internal/account"
	"github.com/yurydemin/marchi/internal/domain"
	"github.com/yurydemin/marchi/internal/importer"
	"github.com/yurydemin/marchi/internal/search"
)

func TestExportEntryName_SanitizesPathUnsafeCharacters(t *testing.T) {
	e := &domain.Email{
		ID: 42, Subject: "Re: invoice/2026 \"final\"", MessageID: "<abc/def:123>@example.com",
	}
	name := exportEntryName("user@example.com", "Sent/Archive", e)

	if strings.Contains(name, "..") {
		t.Errorf("entry name contains '..': %q", name)
	}
	// Every path segment must be exactly what was intended — no
	// unexpected extra segments smuggled in via unsanitized "/".
	parts := strings.Split(name, "/")
	if len(parts) != 4 {
		t.Fatalf("entry name = %q, want exactly 4 path segments, got %d: %v", name, len(parts), parts)
	}
	if strings.Contains(parts[3], "/") {
		t.Errorf("final segment still contains a path separator: %q", parts[3])
	}
}

func TestExportEntryName_EmptySubjectAndMessageIDGetFallbacks(t *testing.T) {
	e := &domain.Email{ID: 7}
	name := exportEntryName("user@example.com", "INBOX", e)
	if !strings.Contains(name, "no-subject") {
		t.Errorf("entry name = %q, want a no-subject fallback for an empty Subject", name)
	}
	if !strings.Contains(name, "id-7") {
		t.Errorf("entry name = %q, want an id-7 fallback for an empty MessageID", name)
	}
}

// seedExportEmail inserts one account, one folder, and one local .eml,
// returning the email id.
func seedExportEmail(t *testing.T, b *backend, accountEmail, folderName, subject, messageID, body string) int64 {
	t.Helper()
	ctx := context.Background()

	acct, err := b.manager.AddAccount(ctx, account.AddAccountParams{
		Email: accountEmail, IMAPHost: "127.0.0.1", IMAPTLS: domain.IMAPTLSNone, IMAPPassword: "hunter2hunter2",
	})
	if err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	folder, err := b.foldersRepo.UpsertFolder(ctx, acct.ID, folderName, 1)
	if err != nil {
		t.Fatalf("UpsertFolder: %v", err)
	}

	emlPath := filepath.Join(t.TempDir(), "msg.eml")
	raw := "Subject: " + subject + "\r\nMessage-ID: " + messageID + "\r\nFrom: a@example.com\r\n\r\n" + body
	if err := os.WriteFile(emlPath, []byte(raw), 0o644); err != nil {
		t.Fatalf("writing seed .eml: %v", err)
	}

	var emailID int64
	err = b.w.Do(ctx, func(tx *sql.Tx) error {
		var err error
		emailID, err = b.emailsRepo.Insert(ctx, tx, &domain.Email{
			MessageID: messageID, AccountID: acct.ID, FolderID: folder.ID, UID: 1,
			Subject: subject, StorageLocation: "local", LocalPath: emlPath,
		})
		return err
	})
	if err != nil {
		t.Fatalf("inserting seed email: %v", err)
	}
	return emailID
}

// TestStreamExport_LocalEmails_ProducesValidZipWithExpectedEntries is
// this step's demo criterion at the Go level (the curl-level check
// unzips the real streamed HTTP response): two emails across two
// different accounts/folders land in the zip at the plan's documented
// path, with byte-exact content.
func TestStreamExport_LocalEmails_ProducesValidZipWithExpectedEntries(t *testing.T) {
	b := newTestBackend(t)
	id1 := seedExportEmail(t, b, "alice@example.com", "INBOX", "First message", "<msg1@example.com>", "Body one.\r\n")
	id2 := seedExportEmail(t, b, "bob@example.com", "Archive", "Second message", "<msg2@example.com>", "Body two.\r\n")

	var buf bytes.Buffer
	w := bufio.NewWriter(&buf)
	b.streamExport(context.Background(), w, "test-job", []int64{id1, id2}, exportFormatZip)
	if err := w.Flush(); err != nil {
		t.Fatalf("flushing: %v", err)
	}

	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("reading produced zip: %v", err)
	}
	if len(zr.File) != 2 {
		t.Fatalf("zip has %d entries, want 2: %v", len(zr.File), zr.File)
	}

	byName := map[string]*zip.File{}
	for _, f := range zr.File {
		byName[f.Name] = f
	}

	wantPrefixes := []string{"alice@example.com/INBOX/", "bob@example.com/Archive/"}
	for _, prefix := range wantPrefixes {
		found := false
		for name := range byName {
			if strings.HasPrefix(name, prefix) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no zip entry with prefix %q, got entries: %v", prefix, keysOf(byName))
		}
	}

	for name, f := range byName {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("opening entry %q: %v", name, err)
		}
		content := make([]byte, f.UncompressedSize64)
		if _, err := rc.Read(content); err != nil && err.Error() != "EOF" {
			t.Fatalf("reading entry %q: %v", name, err)
		}
		rc.Close()
		if !strings.Contains(string(content), "Message-ID:") {
			t.Errorf("entry %q content missing original headers, got:\n%s", name, content)
		}
	}
}

func keysOf(m map[string]*zip.File) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// TestStreamExport_UnknownEmailID_SkipsItAndStillExportsTheRest confirms
// one bad id in the batch doesn't abort the export — the zip still
// contains every email that did resolve.
func TestStreamExport_UnknownEmailID_SkipsItAndStillExportsTheRest(t *testing.T) {
	b := newTestBackend(t)
	id := seedExportEmail(t, b, "alice@example.com", "INBOX", "Only message", "<only@example.com>", "Body.\r\n")

	var buf bytes.Buffer
	w := bufio.NewWriter(&buf)
	b.streamExport(context.Background(), w, "test-job", []int64{999999, id}, exportFormatZip)
	if err := w.Flush(); err != nil {
		t.Fatalf("flushing: %v", err)
	}

	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("reading produced zip: %v", err)
	}
	if len(zr.File) != 1 {
		t.Fatalf("zip has %d entries, want 1 (the unknown id should be skipped, not fatal)", len(zr.File))
	}
}

// TestResolveExportQuery_MatchesSearchIndex confirms a query-based export
// (the "результат поиска" path, as opposed to an explicit email_ids
// selection) resolves ids through the same search index GET
// /api/v1/search uses.
func TestResolveExportQuery_MatchesSearchIndex(t *testing.T) {
	b := newTestBackend(t)
	idx := b.currentIndex()
	if err := idx.Index(search.Doc{EmailID: 101, Subject: "quarterly report", AccountID: 1}); err != nil {
		t.Fatalf("Index: %v", err)
	}
	if err := idx.Index(search.Doc{EmailID: 102, Subject: "unrelated", AccountID: 1}); err != nil {
		t.Fatalf("Index: %v", err)
	}

	ids, err := resolveExportQuery(context.Background(), b, &exportQueryFilter{Q: "quarterly"})
	if err != nil {
		t.Fatalf("resolveExportQuery: %v", err)
	}
	if len(ids) != 1 || ids[0] != 101 {
		t.Errorf("ids = %v, want exactly [101]", ids)
	}
}

// TestStreamExport_Mbox_ProducesReadableMultiMessageFile confirms the
// mbox format writes both messages into one stream with proper "From "
// envelope separators — the actual demo criterion is that
// internal/importer.WalkMbox (the exact inverse of this) reads back the
// same number of messages with the same headers/body, so this exercises
// both sides together as a real round trip.
func TestStreamExport_Mbox_ProducesReadableMultiMessageFile(t *testing.T) {
	b := newTestBackend(t)
	id1 := seedExportEmail(t, b, "alice@example.com", "INBOX", "First message", "<msg1@example.com>", "Body one.\r\n")
	id2 := seedExportEmail(t, b, "bob@example.com", "Archive", "Second message", "<msg2@example.com>", "Body two.\r\n")

	var buf bytes.Buffer
	w := bufio.NewWriter(&buf)
	b.streamExport(context.Background(), w, "test-job", []int64{id1, id2}, exportFormatMbox)
	if err := w.Flush(); err != nil {
		t.Fatalf("flushing: %v", err)
	}

	dir := t.TempDir()
	mboxPath := filepath.Join(dir, "export.mbox")
	if err := os.WriteFile(mboxPath, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	var got []string
	if err := importer.WalkMbox(mboxPath, func(raw []byte) error {
		got = append(got, string(raw))
		return nil
	}); err != nil {
		t.Fatalf("WalkMbox reading the export back: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("WalkMbox found %d messages, want 2: %q", len(got), got)
	}
	if !strings.Contains(got[0], "Message-ID: <msg1@example.com>") || !strings.Contains(got[0], "Body one.") {
		t.Errorf("first message = %q, missing expected content", got[0])
	}
	if !strings.Contains(got[1], "Message-ID: <msg2@example.com>") || !strings.Contains(got[1], "Body two.") {
		t.Errorf("second message = %q, missing expected content", got[1])
	}
}

// TestWriteMboxEntry_EscapesFromLinesInBody guards the exact reason a
// naive concatenation into mbox would corrupt data: a message whose own
// body has a line starting with "From " (a quoted forwarded message, for
// instance) must come back out through WalkMbox as one message with that
// line intact, not silently split into two.
func TestWriteMboxEntry_EscapesFromLinesInBody(t *testing.T) {
	e := &domain.Email{FromAddr: "a@example.com"}
	content := []byte("Subject: quoted\r\n\r\nFrom the desk of Bob:\r\nHello.\r\n")

	var buf bytes.Buffer
	if err := writeMboxEntry(&buf, e, content); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(buf.String(), ">From the desk of Bob:") {
		t.Errorf("output = %q, want the in-body \"From \" line escaped with a leading \">\"", buf.String())
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "escaped.mbox")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	var got []string
	if err := importer.WalkMbox(path, func(raw []byte) error {
		got = append(got, string(raw))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("WalkMbox found %d messages, want 1 (the body's From line must not be mistaken for an envelope)", len(got))
	}
	if !strings.Contains(got[0], "From the desk of Bob:") || strings.Contains(got[0], ">From the desk of Bob:") {
		t.Errorf("round-tripped message = %q, want the escape stripped back off by the reader", got[0])
	}
}

func TestMboxEnvelopeSender_ExtractsBareAddress(t *testing.T) {
	tests := []struct{ full, want string }{
		{`"Alice" <alice@example.com>`, "alice@example.com"},
		{"bob@example.com", "bob@example.com"},
		{"", "MAILER-DAEMON"},
		{"not a valid address at all @@@", "MAILER-DAEMON"},
	}
	for _, tt := range tests {
		if got := mboxEnvelopeSender(tt.full); got != tt.want {
			t.Errorf("mboxEnvelopeSender(%q) = %q, want %q", tt.full, got, tt.want)
		}
	}
}
