package mimeparse

import "testing"

func TestParseAttachments_NoAttachments_PlainTextEmail(t *testing.T) {
	raw := "Subject: plain\r\nFrom: a@example.com\r\nContent-Type: text/plain\r\n\r\nJust text.\r\n"
	got := ParseAttachments([]byte(raw))
	if len(got) != 0 {
		t.Errorf("got %d attachments, want 0: %+v", len(got), got)
	}
}

func TestParseAttachments_OneRealAttachment(t *testing.T) {
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
		"Content-Type: application/pdf\r\n" +
		"Content-Disposition: attachment; filename=\"report.pdf\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n" +
		"\r\n" +
		"UERGLUZJTEUtQ09OVEVOVC1CWVRFUw==\r\n" +
		"--BOUNDARY--\r\n"

	got := ParseAttachments([]byte(raw))
	if len(got) != 1 {
		t.Fatalf("got %d attachments, want 1: %+v", len(got), got)
	}
	a := got[0]
	if a.Filename != "report.pdf" {
		t.Errorf("Filename = %q", a.Filename)
	}
	if a.MIMEType != "application/pdf" {
		t.Errorf("MIMEType = %q", a.MIMEType)
	}
	if a.Size != int64(len("PDF-FILE-CONTENT-BYTES")) {
		t.Errorf("Size = %d, want %d (decoded size, not base64-inflated wire size)", a.Size, len("PDF-FILE-CONTENT-BYTES"))
	}
}

func TestParseAttachments_InlineImageNotCountedAsAttachment(t *testing.T) {
	raw := "Subject: inline image\r\n" +
		"From: a@example.com\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/related; boundary=\"BOUNDARY\"\r\n" +
		"\r\n" +
		"--BOUNDARY\r\n" +
		"Content-Type: text/html\r\n" +
		"\r\n" +
		"<p>See <img src=\"cid:logo123\"></p>\r\n" +
		"--BOUNDARY\r\n" +
		"Content-Type: image/png\r\n" +
		"Content-Disposition: inline; filename=\"logo.png\"\r\n" +
		"Content-Id: <logo123>\r\n" +
		"Content-Transfer-Encoding: base64\r\n" +
		"\r\n" +
		"UE5HLUlNQUdFLUJZVEVTLUhFUkU=\r\n" +
		"--BOUNDARY--\r\n"

	got := ParseAttachments([]byte(raw))
	if len(got) != 0 {
		t.Errorf("got %d attachments, want 0 (inline image is body content, not an attachment): %+v", len(got), got)
	}
}

func TestParseAttachments_MissingFilenameGetsFallback(t *testing.T) {
	raw := "Subject: no filename\r\n" +
		"From: a@example.com\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=\"BOUNDARY\"\r\n" +
		"\r\n" +
		"--BOUNDARY\r\n" +
		"Content-Type: text/plain\r\n" +
		"\r\n" +
		"body\r\n" +
		"--BOUNDARY\r\n" +
		"Content-Type: application/octet-stream\r\n" +
		"Content-Disposition: attachment\r\n" +
		"Content-Transfer-Encoding: base64\r\n" +
		"\r\n" +
		"UERGLUZJTEUtQ09OVEVOVC1CWVRFUw==\r\n" +
		"--BOUNDARY--\r\n"

	got := ParseAttachments([]byte(raw))
	if len(got) != 1 {
		t.Fatalf("got %d attachments, want 1", len(got))
	}
	if got[0].Filename != "attachment-1" {
		t.Errorf("Filename = %q, want fallback attachment-1", got[0].Filename)
	}
}

func TestParseAttachments_MultipleAttachments(t *testing.T) {
	raw := "Subject: two files\r\n" +
		"From: a@example.com\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=\"BOUNDARY\"\r\n" +
		"\r\n" +
		"--BOUNDARY\r\n" +
		"Content-Type: text/plain\r\n" +
		"\r\n" +
		"body\r\n" +
		"--BOUNDARY\r\n" +
		"Content-Type: application/pdf\r\n" +
		"Content-Disposition: attachment; filename=\"one.pdf\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n" +
		"\r\n" +
		"UERGLUZJTEUtQ09OVEVOVC1CWVRFUw==\r\n" +
		"--BOUNDARY\r\n" +
		"Content-Type: image/png\r\n" +
		"Content-Disposition: attachment; filename=\"two.png\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n" +
		"\r\n" +
		"UE5HLUlNQUdFLUJZVEVTLUhFUkU=\r\n" +
		"--BOUNDARY--\r\n"

	got := ParseAttachments([]byte(raw))
	if len(got) != 2 {
		t.Fatalf("got %d attachments, want 2: %+v", len(got), got)
	}
	if got[0].Filename != "one.pdf" || got[1].Filename != "two.png" {
		t.Errorf("filenames = %q, %q", got[0].Filename, got[1].Filename)
	}
}

func TestParseAttachments_EmptyInput_DoesNotPanic(t *testing.T) {
	got := ParseAttachments([]byte(""))
	if got != nil {
		t.Errorf("got %v, want nil for empty input", got)
	}
}

func TestParseAttachments_GarbageInput_DoesNotPanic(t *testing.T) {
	got := ParseAttachments([]byte("not an email\x00\x01\x02"))
	_ = got // no panic is the assertion
}

func twoAttachmentsMessage() []byte {
	return []byte("Subject: two attachments\r\n" +
		"From: a@example.com\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=\"BOUNDARY\"\r\n" +
		"\r\n" +
		"--BOUNDARY\r\n" +
		"Content-Type: text/plain\r\n" +
		"\r\n" +
		"See attached.\r\n" +
		"--BOUNDARY\r\n" +
		"Content-Type: application/pdf\r\n" +
		"Content-Disposition: attachment; filename=\"one.pdf\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n" +
		"\r\n" +
		"Rmlyc3QgYXR0YWNobWVudA==\r\n" + // "First attachment"
		"--BOUNDARY\r\n" +
		"Content-Type: image/png\r\n" +
		"Content-Disposition: attachment; filename=\"two.png\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n" +
		"\r\n" +
		"U2Vjb25kIGF0dGFjaG1lbnQ=\r\n" + // "Second attachment"
		"--BOUNDARY--\r\n")
}

func TestExtractAttachmentAt_ReturnsCorrectContentByPosition(t *testing.T) {
	raw := twoAttachmentsMessage()

	first, ok := ExtractAttachmentAt(raw, 0)
	if !ok {
		t.Fatal("index 0: ok = false")
	}
	if string(first) != "First attachment" {
		t.Errorf("index 0 content = %q, want %q", first, "First attachment")
	}

	second, ok := ExtractAttachmentAt(raw, 1)
	if !ok {
		t.Fatal("index 1: ok = false")
	}
	if string(second) != "Second attachment" {
		t.Errorf("index 1 content = %q, want %q", second, "Second attachment")
	}
}

func TestExtractAttachmentAt_MatchesParseAttachmentsOrder(t *testing.T) {
	raw := twoAttachmentsMessage()
	meta := ParseAttachments(raw)
	if len(meta) != 2 {
		t.Fatalf("got %d attachments, want 2", len(meta))
	}

	for i, m := range meta {
		content, ok := ExtractAttachmentAt(raw, i)
		if !ok {
			t.Fatalf("index %d: ok = false", i)
		}
		if int64(len(content)) != m.Size {
			t.Errorf("index %d (%s): content length %d, want ParseAttachments' reported Size %d",
				i, m.Filename, len(content), m.Size)
		}
	}
}

func TestExtractAttachmentAt_OutOfRange(t *testing.T) {
	raw := twoAttachmentsMessage()
	_, ok := ExtractAttachmentAt(raw, 2)
	if ok {
		t.Error("expected ok = false for an out-of-range index")
	}
}

func TestExtractAttachmentAt_EmptyInput_DoesNotPanic(t *testing.T) {
	_, ok := ExtractAttachmentAt([]byte(""), 0)
	if ok {
		t.Error("expected ok = false for empty input")
	}
}
