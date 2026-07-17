package mimeparse

import (
	"testing"
	"time"
)

func TestParse_WellFormedMessage(t *testing.T) {
	raw := "Message-Id: <abc123@example.com>\r\n" +
		"Subject: Hello there\r\n" +
		"From: Alice <alice@example.com>\r\n" +
		"To: Bob <bob@example.com>\r\n" +
		"Cc: Carol <carol@example.com>\r\n" +
		"Date: Mon, 2 Jan 2006 15:04:05 +0000\r\n" +
		"\r\n" +
		"Body text.\r\n"

	md := Parse([]byte(raw))

	if md.MessageID != "abc123@example.com" {
		t.Errorf("MessageID = %q", md.MessageID)
	}
	if md.Subject != "Hello there" {
		t.Errorf("Subject = %q", md.Subject)
	}
	// net/mail.Address.String() (which Address is a type alias for) quotes
	// display names per RFC 5322 — "Alice" <alice@example.com>, not bare.
	if md.From != `"Alice" <alice@example.com>` {
		t.Errorf("From = %q", md.From)
	}
	if len(md.To) != 1 || md.To[0] != `"Bob" <bob@example.com>` {
		t.Errorf("To = %v", md.To)
	}
	if len(md.Cc) != 1 || md.Cc[0] != `"Carol" <carol@example.com>` {
		t.Errorf("Cc = %v", md.Cc)
	}
	want := time.Date(2006, 1, 2, 15, 4, 5, 0, time.UTC)
	if !md.Date.Equal(want) {
		t.Errorf("Date = %v, want %v", md.Date, want)
	}
}

func TestParse_MultipleRecipients(t *testing.T) {
	raw := "Subject: multi\r\n" +
		"From: a@example.com\r\n" +
		"To: b@example.com, c@example.com, d@example.com\r\n" +
		"\r\n" +
		"body\r\n"

	md := Parse([]byte(raw))
	if len(md.To) != 3 {
		t.Fatalf("got %d To addresses, want 3: %v", len(md.To), md.To)
	}
}

func TestParse_EncodedWordSubject(t *testing.T) {
	// RFC 2047 encoded-word: "Привет" (UTF-8, base64).
	raw := "Subject: =?UTF-8?B?0J/RgNC40LLQtdGC?=\r\n" +
		"From: a@example.com\r\n" +
		"\r\n" +
		"body\r\n"

	md := Parse([]byte(raw))
	if md.Subject != "Привет" {
		t.Errorf("Subject = %q, want decoded %q", md.Subject, "Привет")
	}
}

func TestParse_MissingMessageID(t *testing.T) {
	raw := "Subject: no message id\r\n" +
		"From: a@example.com\r\n" +
		"\r\n" +
		"body\r\n"

	md := Parse([]byte(raw))
	if md.MessageID != "" {
		t.Errorf("MessageID = %q, want empty for a message with no Message-Id header", md.MessageID)
	}
}

func TestParse_MissingHeaders(t *testing.T) {
	raw := "Subject: only a subject\r\n\r\nbody\r\n"

	md := Parse([]byte(raw))
	if md.Subject != "only a subject" {
		t.Errorf("Subject = %q", md.Subject)
	}
	if md.From != "" {
		t.Errorf("From = %q, want empty", md.From)
	}
	if md.To != nil {
		t.Errorf("To = %v, want nil", md.To)
	}
}

func TestParse_EmptyInput_DoesNotPanic(t *testing.T) {
	md := Parse([]byte(""))
	if md.Subject != "" || md.MessageID != "" {
		t.Errorf("expected empty Metadata for empty input, got %+v", md)
	}
}

func TestParse_GarbageInput_DoesNotPanic(t *testing.T) {
	md := Parse([]byte("this is not a valid email at all\x00\x01\x02"))
	// No specific assertion beyond "doesn't panic" — malformed input is
	// expected to yield a best-effort (possibly empty) Metadata, matching
	// FR-SE-04's "never block archiving over a parse failure" contract.
	_ = md
}

func TestParse_NoBlankLineBeforeBody(t *testing.T) {
	// A message with only headers and no body at all must still parse the
	// headers rather than erroring out.
	raw := "Subject: header only\r\nFrom: a@example.com\r\n\r\n"
	md := Parse([]byte(raw))
	if md.Subject != "header only" {
		t.Errorf("Subject = %q", md.Subject)
	}
}
