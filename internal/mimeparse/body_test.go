package mimeparse

import (
	"strings"
	"testing"
)

func TestParseBody_PlainTextOnly(t *testing.T) {
	raw := "Subject: plain\r\nFrom: a@example.com\r\nContent-Type: text/plain\r\n\r\nHello, this is the body.\r\n"
	got := ParseBody([]byte(raw))
	if !strings.Contains(got, "Hello, this is the body.") {
		t.Errorf("ParseBody() = %q, want it to contain the plain text body", got)
	}
}

func TestParseBody_HTMLTagsStripped(t *testing.T) {
	raw := "Subject: html\r\n" +
		"From: a@example.com\r\n" +
		"Content-Type: text/html\r\n" +
		"\r\n" +
		"<html><body><p>Hello <b>World</b></p></body></html>\r\n"

	got := ParseBody([]byte(raw))
	if strings.ContainsAny(got, "<>") {
		t.Errorf("ParseBody() = %q, want no HTML tags", got)
	}
	if !strings.Contains(got, "Hello") || !strings.Contains(got, "World") {
		t.Errorf("ParseBody() = %q, want it to contain the visible text", got)
	}
}

func TestParseBody_SkipsScriptAndStyleContent(t *testing.T) {
	raw := "Subject: html with script\r\n" +
		"From: a@example.com\r\n" +
		"Content-Type: text/html\r\n" +
		"\r\n" +
		"<html><head><style>.x{color:red}</style><script>alert('x')</script></head>" +
		"<body>Visible text</body></html>\r\n"

	got := ParseBody([]byte(raw))
	if strings.Contains(got, "alert") || strings.Contains(got, "color:red") {
		t.Errorf("ParseBody() = %q, want script/style content excluded", got)
	}
	if !strings.Contains(got, "Visible text") {
		t.Errorf("ParseBody() = %q, want the visible body text", got)
	}
}

func TestParseBody_MultipartAlternative_IncludesBothParts(t *testing.T) {
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

	got := ParseBody([]byte(raw))
	if !strings.Contains(got, "Plain version.") {
		t.Errorf("ParseBody() = %q, want the text/plain part included", got)
	}
	if !strings.Contains(got, "HTML version.") {
		t.Errorf("ParseBody() = %q, want the text/html part's text included", got)
	}
}

func TestParseBody_ExcludesAttachmentContent(t *testing.T) {
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

	got := ParseBody([]byte(raw))
	if strings.Contains(got, "SECRET ATTACHMENT CONTENT") {
		t.Errorf("ParseBody() = %q, want attachment content excluded from the indexed body", got)
	}
	if !strings.Contains(got, "See attached.") {
		t.Errorf("ParseBody() = %q, want the inline body text included", got)
	}
}
