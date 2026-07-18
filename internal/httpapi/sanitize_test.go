package httpapi

import (
	"strings"
	"testing"
)

func TestSanitizeEmailHTML_StripsScriptTag(t *testing.T) {
	got := sanitizeEmailHTML(`<p>hi</p><script>alert('xss')</script>`)
	if strings.Contains(got, "script") || strings.Contains(got, "alert") {
		t.Errorf("sanitized = %q, want no script content", got)
	}
	if !strings.Contains(got, "hi") {
		t.Errorf("sanitized = %q, want the safe text preserved", got)
	}
}

func TestSanitizeEmailHTML_StripsOnClickAttribute(t *testing.T) {
	got := sanitizeEmailHTML(`<a href="https://example.com" onclick="steal()">link</a>`)
	if strings.Contains(got, "onclick") || strings.Contains(got, "steal") {
		t.Errorf("sanitized = %q, want onclick stripped", got)
	}
	if !strings.Contains(got, "href=") {
		t.Errorf("sanitized = %q, want the safe href preserved", got)
	}
}

func TestSanitizeEmailHTML_StripsExternalImage(t *testing.T) {
	got := sanitizeEmailHTML(`<p>text</p><img src="https://tracker.example.com/pixel.gif">`)
	if strings.Contains(got, "<img") || strings.Contains(got, "tracker.example.com") {
		t.Errorf("sanitized = %q, want the external image removed entirely", got)
	}
}

func TestSanitizeEmailHTML_StripsDataURIImage(t *testing.T) {
	got := sanitizeEmailHTML(`<img src="data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAAB">`)
	if strings.Contains(got, "<img") || strings.Contains(got, "data:") {
		t.Errorf("sanitized = %q, want the data URI image removed entirely", got)
	}
}

func TestSanitizeEmailHTML_StripsIframeAndObject(t *testing.T) {
	got := sanitizeEmailHTML(`<iframe src="https://evil.example.com"></iframe><object data="x.swf"></object>`)
	if strings.Contains(got, "iframe") || strings.Contains(got, "object") {
		t.Errorf("sanitized = %q, want iframe/object removed", got)
	}
}

func TestSanitizeEmailHTML_StripsDataURILink(t *testing.T) {
	got := sanitizeEmailHTML(`<a href="data:text/html,<script>alert(1)</script>">click</a>`)
	if strings.Contains(got, "data:") {
		t.Errorf("sanitized = %q, want the data: href scheme rejected", got)
	}
}

func TestSanitizeEmailHTML_PreservesSafeFormatting(t *testing.T) {
	got := sanitizeEmailHTML(`<p>Hello <b>World</b>, see <a href="https://example.com">this</a>.</p>`)
	for _, want := range []string{"Hello", "<b>World</b>", "href=", "example.com"} {
		if !strings.Contains(got, want) {
			t.Errorf("sanitized = %q, want it to contain %q", got, want)
		}
	}
}

func TestSanitizeEmailHTML_AddsNoFollowToLinks(t *testing.T) {
	got := sanitizeEmailHTML(`<a href="https://example.com">link</a>`)
	if !strings.Contains(got, `rel="nofollow`) {
		t.Errorf("sanitized = %q, want rel=nofollow added to the link", got)
	}
}

func TestSanitizeEmailHTML_EmptyInput(t *testing.T) {
	if got := sanitizeEmailHTML(""); got != "" {
		t.Errorf("sanitized = %q, want empty for empty input", got)
	}
}
