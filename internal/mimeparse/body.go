package mimeparse

import (
	"bytes"
	"io"
	"strings"

	"golang.org/x/net/html"

	"github.com/emersion/go-message"
	"github.com/emersion/go-message/mail"
)

// ParseBody extracts the plain-text content of raw suitable for full-text
// indexing (FR-SR-02: "body ... только text/plain и text/html без
// тегов"): every text/plain part verbatim, plus every text/html part with
// tags stripped, concatenated together. This is index content only — it
// never touches the archived .eml itself, and nothing here gets stored
// separately in SQLite.
func ParseBody(raw []byte) string {
	r, err := mail.CreateReader(bytes.NewReader(raw))
	if r == nil {
		return ""
	}
	if err != nil && !message.IsUnknownCharset(err) && !message.IsUnknownEncoding(err) {
		return ""
	}
	defer r.Close()

	var sb strings.Builder
	for {
		part, partErr := r.NextPart()
		if partErr == io.EOF {
			break
		}
		if partErr != nil && !message.IsUnknownCharset(partErr) && !message.IsUnknownEncoding(partErr) {
			break // malformed beyond what's recoverable — stop, don't loop forever
		}

		ih, ok := part.Header.(*mail.InlineHeader)
		if !ok {
			continue // an AttachmentHeader part — not body text
		}

		content, err := io.ReadAll(part.Body)
		if err != nil && len(content) == 0 {
			continue
		}

		contentType, _, _ := ih.ContentType()
		switch {
		case strings.HasPrefix(contentType, "text/html"):
			sb.WriteString(stripHTMLTags(content))
		case contentType == "" || strings.HasPrefix(contentType, "text/plain"):
			sb.Write(content)
		default:
			continue // some other inline content-type — not indexable text
		}
		sb.WriteByte('\n')
	}
	return sb.String()
}

// stripHTMLTags returns just the text nodes of an HTML document, skipping
// <script>/<style> contents entirely — a proper tokenizer rather than a
// tag-stripping regex, so malformed markup or a stray "<" in body text
// doesn't corrupt the extracted text.
func stripHTMLTags(htmlContent []byte) string {
	var sb strings.Builder
	skipDepth := 0 // >0 while inside a <script> or <style> element

	z := html.NewTokenizer(bytes.NewReader(htmlContent))
	for {
		switch z.Next() {
		case html.ErrorToken:
			return sb.String()
		case html.StartTagToken:
			name, _ := z.TagName()
			if isSkippedTag(name) {
				skipDepth++
			}
		case html.EndTagToken:
			name, _ := z.TagName()
			if isSkippedTag(name) && skipDepth > 0 {
				skipDepth--
			}
		case html.TextToken:
			if skipDepth == 0 {
				sb.Write(z.Text())
				sb.WriteByte(' ')
			}
		}
	}
}

func isSkippedTag(name []byte) bool {
	return bytes.Equal(name, []byte("script")) || bytes.Equal(name, []byte("style"))
}
