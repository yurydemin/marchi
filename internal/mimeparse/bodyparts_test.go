package mimeparse

import (
	"strings"
	"testing"
)

func TestParseBodyParts_PlainTextOnly_NoHTML(t *testing.T) {
	raw := "Subject: plain\r\nFrom: a@example.com\r\nContent-Type: text/plain\r\n\r\nHello there.\r\n"
	parts := ParseBodyParts([]byte(raw))
	if parts.HTML != "" {
		t.Errorf("HTML = %q, want empty (no HTML part in the message)", parts.HTML)
	}
	if !strings.Contains(parts.Text, "Hello there.") {
		t.Errorf("Text = %q, want it to contain the plain body", parts.Text)
	}
}

func TestParseBodyParts_HTMLIsReturnedRawNotStripped(t *testing.T) {
	raw := "Subject: html\r\n" +
		"From: a@example.com\r\n" +
		"Content-Type: text/html\r\n" +
		"\r\n" +
		"<p>Hello <b>World</b></p>\r\n"

	parts := ParseBodyParts([]byte(raw))
	if !strings.Contains(parts.HTML, "<b>World</b>") {
		t.Errorf("HTML = %q, want the raw markup preserved (sanitization happens elsewhere)", parts.HTML)
	}
}

func TestParseBodyParts_MultipartAlternative_KeepsBothSeparate(t *testing.T) {
	raw := "Subject: alt\r\n" +
		"From: a@example.com\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/alternative; boundary=\"BOUNDARY\"\r\n" +
		"\r\n" +
		"--BOUNDARY\r\n" +
		"Content-Type: text/plain\r\n" +
		"\r\n" +
		"Plain version.\r\n" +
		"--BOUNDARY\r\n" +
		"Content-Type: text/html\r\n" +
		"\r\n" +
		"<p>HTML version.</p>\r\n" +
		"--BOUNDARY--\r\n"

	parts := ParseBodyParts([]byte(raw))
	if !strings.Contains(parts.Text, "Plain version.") {
		t.Errorf("Text = %q, want the text/plain part", parts.Text)
	}
	if !strings.Contains(parts.HTML, "<p>HTML version.</p>") {
		t.Errorf("HTML = %q, want the raw text/html part", parts.HTML)
	}
	if strings.Contains(parts.Text, "HTML version") {
		t.Error("HTML content leaked into Text")
	}
	if strings.Contains(parts.HTML, "Plain version") {
		t.Error("plain content leaked into HTML")
	}
}

func TestParseBodyParts_ExcludesAttachmentContent(t *testing.T) {
	raw := "Subject: with attachment\r\n" +
		"From: a@example.com\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=\"BOUNDARY\"\r\n" +
		"\r\n" +
		"--BOUNDARY\r\n" +
		"Content-Type: text/plain\r\n" +
		"\r\n" +
		"See attached.\r\n" +
		"--BOUNDARY\r\n" +
		"Content-Type: text/plain\r\n" +
		"Content-Disposition: attachment; filename=\"notes.txt\"\r\n" +
		"\r\n" +
		"SECRET ATTACHMENT CONTENT\r\n" +
		"--BOUNDARY--\r\n"

	parts := ParseBodyParts([]byte(raw))
	if strings.Contains(parts.Text, "SECRET ATTACHMENT CONTENT") {
		t.Errorf("Text = %q, want attachment content excluded", parts.Text)
	}
	if !strings.Contains(parts.Text, "See attached.") {
		t.Errorf("Text = %q, want the inline body text", parts.Text)
	}
}

func TestParseBodyParts_EmptyInput_DoesNotPanic(t *testing.T) {
	parts := ParseBodyParts(nil)
	if parts.HTML != "" || parts.Text != "" {
		t.Errorf("parts = %+v, want zero value for empty input", parts)
	}
}
