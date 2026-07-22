package imapclient

import (
	"context"
	"testing"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"

	"github.com/yurydemin/marchi/internal/domain"
	"github.com/yurydemin/marchi/internal/testutil/dovecot"
)

// TestAppend_RealDovecot covers Phase 4 step 6's coverage gap: Append
// itself was previously only exercised indirectly through
// internal/restore's dovecot tests (via the Restore Engine), never
// directly in this package. Appends a message, then fetches it back by
// a completely separate connection to confirm content, date, and flags
// all round-tripped.
func TestAppend_RealDovecot(t *testing.T) {
	srv := dovecot.Start(t, "append-test@dovecot.local", "pass12345")
	c := dialAndLogin(t, srv, "append-test@dovecot.local", "pass12345")
	defer c.Logout()

	raw := []byte("From: sender@example.com\r\nSubject: append test\r\nMessage-ID: <append-test@example.com>\r\n\r\nBody.\r\n")
	when := time.Date(2026, 5, 1, 12, 30, 0, 0, time.UTC)
	if err := Append(c, "INBOX", []string{imap.SeenFlag, imap.FlaggedFlag}, when, raw); err != nil {
		t.Fatalf("Append: %v", err)
	}

	subject, flags, date := fetchOneEnvelope(t, srv, "append-test@dovecot.local", "pass12345", "append-test@example.com")
	if subject != "append test" {
		t.Errorf("Subject = %q, want %q", subject, "append test")
	}
	if !containsFlag(flags, imap.SeenFlag) || !containsFlag(flags, imap.FlaggedFlag) {
		t.Errorf("Flags = %v, want \\Seen and \\Flagged preserved", flags)
	}
	if !date.Equal(when) {
		t.Errorf("INTERNALDATE = %v, want %v", date, when)
	}
}

// TestMarkSeen_RealDovecot covers FR-RE-03's archive_and_mark_read path:
// a message appended without \Seen, then marked seen by UID, confirmed
// via an independent FETCH.
func TestMarkSeen_RealDovecot(t *testing.T) {
	srv := dovecot.Start(t, "markseen-test@dovecot.local", "pass12345")
	srv.AppendMessage(t, "markseen-test@dovecot.local", "pass12345", "INBOX",
		[]byte("From: sender@example.com\r\nSubject: mark seen test\r\nMessage-ID: <markseen-test@example.com>\r\n\r\nBody.\r\n"))

	c := dialAndLogin(t, srv, "markseen-test@dovecot.local", "pass12345")
	defer c.Logout()
	if _, err := c.Select("INBOX", false); err != nil {
		t.Fatalf("SELECT INBOX: %v", err)
	}

	uid := findUIDByMessageID(t, c, "markseen-test@example.com")
	if err := MarkSeen(c, uid); err != nil {
		t.Fatalf("MarkSeen: %v", err)
	}

	_, flags, _ := fetchOneEnvelope(t, srv, "markseen-test@dovecot.local", "pass12345", "markseen-test@example.com")
	if !containsFlag(flags, imap.SeenFlag) {
		t.Errorf("Flags = %v, want \\Seen after MarkSeen", flags)
	}
}

// TestDeleteMessage_RealDovecot covers FR-RE-03's archive_and_delete
// path: DeleteMessage flags \Deleted and expunges, so the message
// actually disappears from the mailbox — not just gets flagged.
func TestDeleteMessage_RealDovecot(t *testing.T) {
	srv := dovecot.Start(t, "delete-test@dovecot.local", "pass12345")
	srv.AppendMessage(t, "delete-test@dovecot.local", "pass12345", "INBOX",
		[]byte("From: sender@example.com\r\nSubject: keep me\r\nMessage-ID: <keep-me@example.com>\r\n\r\nBody.\r\n"))
	srv.AppendMessage(t, "delete-test@dovecot.local", "pass12345", "INBOX",
		[]byte("From: sender@example.com\r\nSubject: delete me\r\nMessage-ID: <delete-me@example.com>\r\n\r\nBody.\r\n"))

	c := dialAndLogin(t, srv, "delete-test@dovecot.local", "pass12345")
	defer c.Logout()
	if _, err := c.Select("INBOX", false); err != nil {
		t.Fatalf("SELECT INBOX: %v", err)
	}

	uid := findUIDByMessageID(t, c, "delete-me@example.com")
	if err := DeleteMessage(c, uid); err != nil {
		t.Fatalf("DeleteMessage: %v", err)
	}

	mbox, err := c.Select("INBOX", false)
	if err != nil {
		t.Fatalf("re-SELECT INBOX: %v", err)
	}
	if mbox.Messages != 1 {
		t.Errorf("mailbox has %d messages after delete, want 1", mbox.Messages)
	}
}

func dialAndLogin(t *testing.T, srv *dovecot.Server, user, pass string) *client.Client {
	t.Helper()
	c, err := Connect(context.Background(), ConnectOptions{
		Host: srv.Host, Port: srv.Port, TLS: domain.IMAPTLSNone,
		Username: user, Password: pass, DialTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	return c
}

// fetchOneEnvelope dials srv independently (not reusing the connection
// under test), SELECTs INBOX, and FETCHes the one message whose
// Message-ID contains messageIDFragment — returning enough to assert
// Append/MarkSeen actually took effect from a client that never issued
// the mutation itself.
func fetchOneEnvelope(t *testing.T, srv *dovecot.Server, user, pass, messageIDFragment string) (subject string, flags []string, date time.Time) {
	t.Helper()
	c := dialAndLogin(t, srv, user, pass)
	defer c.Logout()

	if _, err := c.Select("INBOX", false); err != nil {
		t.Fatalf("SELECT INBOX: %v", err)
	}

	seqset := new(imap.SeqSet)
	seqset.AddRange(1, 0)
	messages := make(chan *imap.Message, 10)
	done := make(chan error, 1)
	go func() {
		done <- c.Fetch(seqset, []imap.FetchItem{imap.FetchEnvelope, imap.FetchFlags, imap.FetchInternalDate}, messages)
	}()

	var found *imap.Message
	for msg := range messages {
		if msg.Envelope != nil && containsSubstr(msg.Envelope.MessageId, messageIDFragment) {
			found = msg
		}
	}
	if err := <-done; err != nil {
		t.Fatalf("FETCH: %v", err)
	}
	if found == nil {
		t.Fatalf("no message with Message-ID containing %q found", messageIDFragment)
	}
	return found.Envelope.Subject, found.Flags, found.InternalDate
}

// findUIDByMessageID assumes mailbox is already SELECTed on c, and
// returns the UID of the one message whose Message-ID contains
// messageIDFragment.
func findUIDByMessageID(t *testing.T, c *client.Client, messageIDFragment string) uint32 {
	t.Helper()
	seqset := new(imap.SeqSet)
	seqset.AddRange(1, 0)
	messages := make(chan *imap.Message, 10)
	done := make(chan error, 1)
	go func() {
		done <- c.Fetch(seqset, []imap.FetchItem{imap.FetchUid, imap.FetchEnvelope}, messages)
	}()

	var uid uint32
	for msg := range messages {
		if msg.Envelope != nil && containsSubstr(msg.Envelope.MessageId, messageIDFragment) {
			uid = msg.Uid
		}
	}
	if err := <-done; err != nil {
		t.Fatalf("FETCH: %v", err)
	}
	if uid == 0 {
		t.Fatalf("no message with Message-ID containing %q found", messageIDFragment)
	}
	return uid
}

func containsFlag(flags []string, want string) bool {
	for _, f := range flags {
		if f == want {
			return true
		}
	}
	return false
}

func containsSubstr(s, substr string) bool {
	return len(s) >= len(substr) && (func() bool {
		for i := 0; i+len(substr) <= len(s); i++ {
			if s[i:i+len(substr)] == substr {
				return true
			}
		}
		return false
	})()
}
