package webui

import (
	"bytes"
	"io/fs"
	"strings"
	"testing"
	"time"
)

// testStats mirrors the shape internal/httpapi's statsResponse feeds the
// "index" page with (Unlocked branch). Kept local rather than importing
// internal/httpapi, which itself imports this package.
type testStats struct {
	TotalEmails       int
	TotalAccounts     int
	ActiveAccounts    int
	LocalStorageBytes int64
	Accounts          []testAccountStats
}

type testAccountStats struct {
	Email          string
	IsActive       bool
	EmailCount     int
	LastSyncStatus string
	LastSyncAt     *time.Time
}

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
	syncedAt := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name     string
		unlocked bool
		want     string
		notWant  string
	}{
		{"locked", false, "Unlock MailVault", "Dashboard"},
		{"unlocked", true, "Dashboard", "Unlock MailVault"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			data := struct {
				Unlocked bool
				Stats    testStats
			}{
				Unlocked: tt.unlocked,
				Stats: testStats{
					TotalEmails:       42,
					TotalAccounts:     2,
					ActiveAccounts:    1,
					LocalStorageBytes: 123456,
					Accounts: []testAccountStats{
						{Email: "a@example.com", IsActive: true, EmailCount: 40, LastSyncStatus: "success", LastSyncAt: &syncedAt},
						{Email: "b@example.com", IsActive: false, EmailCount: 2},
					},
				},
			}
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
