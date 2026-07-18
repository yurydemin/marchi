package webui

import (
	"bytes"
	"io/fs"
	"strings"
	"testing"
)

func TestParse_ReturnsIndexPage(t *testing.T) {
	pages, err := Parse()
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if _, ok := pages["index"]; !ok {
		t.Fatal(`Parse() result is missing the "index" page`)
	}
}

func TestParse_IndexPage_RendersLockedAndUnlockedContent(t *testing.T) {
	pages, err := Parse()
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	index := pages["index"]

	tests := []struct {
		name     string
		unlocked bool
		want     string
		notWant  string
	}{
		{"locked", false, "Unlock MailVault", "Vault unlocked"},
		{"unlocked", true, "Vault unlocked", "Unlock MailVault"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			data := struct{ Unlocked bool }{Unlocked: tt.unlocked}
			if err := index.ExecuteTemplate(&buf, "layout", data); err != nil {
				t.Fatalf("ExecuteTemplate: %v", err)
			}
			out := buf.String()
			if !strings.Contains(out, tt.want) {
				t.Errorf("rendered output missing %q, got:\n%s", tt.want, out)
			}
			if strings.Contains(out, tt.notWant) {
				t.Errorf("rendered output unexpectedly contains %q, got:\n%s", tt.notWant, out)
			}
		})
	}
}

func TestStaticFS_ContainsExpectedAssets(t *testing.T) {
	sub := StaticFS()
	for _, path := range []string{"css/app.css", "js/htmx.min.js", "js/app.js"} {
		if _, err := fs.Stat(sub, path); err != nil {
			t.Errorf("StaticFS() is missing %q: %v", path, err)
		}
	}
}
