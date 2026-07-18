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

// testAccount mirrors the exported fields internal/httpapi's
// accountResponse feeds the "accounts" page's fragments with.
type testAccount struct {
	ID           int64
	Email        string
	DisplayName  string
	IMAPHost     string
	IMAPPort     int
	IMAPTLS      string
	IMAPUsername string
	IsActive     bool
}

func TestParse_ReturnsAccountsPage(t *testing.T) {
	pages, err := Parse()
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if _, ok := pages["accounts"]; !ok {
		t.Fatal(`Parse() result is missing the "accounts" page`)
	}
}

func TestParse_AccountsPage_RendersEmptyAndPopulatedLists(t *testing.T) {
	pages, err := Parse()
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	accounts := pages["accounts"]

	t.Run("empty", func(t *testing.T) {
		var buf bytes.Buffer
		data := struct {
			Unlocked bool
			Accounts []testAccount
		}{Unlocked: true}
		if err := accounts.ExecuteTemplate(&buf, "layout", data); err != nil {
			t.Fatalf("ExecuteTemplate: %v", err)
		}
		if !strings.Contains(buf.String(), "No accounts yet") {
			t.Errorf("empty-state output missing the empty-state message, got:\n%s", buf.String())
		}
	})

	t.Run("populated", func(t *testing.T) {
		var buf bytes.Buffer
		data := struct {
			Unlocked bool
			Accounts []testAccount
		}{
			Unlocked: true,
			Accounts: []testAccount{
				{ID: 1, Email: "a@example.com", IMAPHost: "imap.example.com", IMAPPort: 993, IMAPTLS: "ssl", IsActive: true},
			},
		}
		if err := accounts.ExecuteTemplate(&buf, "layout", data); err != nil {
			t.Fatalf("ExecuteTemplate: %v", err)
		}
		out := buf.String()
		if !strings.Contains(out, "a@example.com") {
			t.Errorf("populated output missing the account's email, got:\n%s", out)
		}
	})
}

// TestParse_AccountRowsFragment_HandlesEmptyToPopulatedTransition guards
// the bug caught during manual verification: create/update/delete/toggle
// all re-render "account-rows" (a <tbody> replacement) rather than
// appending or outerHTML-swapping a single row, specifically because the
// empty state has to render as a <tr> placeholder inside the same
// <tbody> — not a sibling <div> — or the first real row lands outside
// any <table> once appended.
func TestParse_AccountRowsFragment_HandlesEmptyToPopulatedTransition(t *testing.T) {
	pages, err := Parse()
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	accounts := pages["accounts"]

	t.Run("empty", func(t *testing.T) {
		var buf bytes.Buffer
		if err := accounts.ExecuteTemplate(&buf, "account-rows", []testAccount{}); err != nil {
			t.Fatalf("ExecuteTemplate: %v", err)
		}
		out := buf.String()
		if !strings.Contains(out, "<tr>") || !strings.Contains(out, "No accounts yet") {
			t.Errorf("empty account-rows must render a <tr> placeholder, got:\n%s", out)
		}
	})

	t.Run("populated", func(t *testing.T) {
		var buf bytes.Buffer
		data := []testAccount{{ID: 1, Email: "a@example.com", IsActive: true}}
		if err := accounts.ExecuteTemplate(&buf, "account-rows", data); err != nil {
			t.Fatalf("ExecuteTemplate: %v", err)
		}
		out := buf.String()
		if strings.Contains(out, "No accounts yet") {
			t.Errorf("populated account-rows must not show the empty-state placeholder, got:\n%s", out)
		}
		if !strings.Contains(out, "a@example.com") {
			t.Errorf("populated account-rows missing the account's email, got:\n%s", out)
		}
	})
}

func TestParse_AccountsPage_RowAndEditRowFragmentsRenderIndependently(t *testing.T) {
	pages, err := Parse()
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	accounts := pages["accounts"]
	a := testAccount{ID: 7, Email: "row@example.com", IMAPHost: "imap.example.com", IMAPPort: 993, IMAPTLS: "starttls", IsActive: false}

	t.Run("account-row", func(t *testing.T) {
		var buf bytes.Buffer
		if err := accounts.ExecuteTemplate(&buf, "account-row", a); err != nil {
			t.Fatalf("ExecuteTemplate: %v", err)
		}
		if !strings.Contains(buf.String(), "row@example.com") {
			t.Errorf("account-row fragment missing the email, got:\n%s", buf.String())
		}
	})

	t.Run("account-edit-row", func(t *testing.T) {
		var buf bytes.Buffer
		if err := accounts.ExecuteTemplate(&buf, "account-edit-row", a); err != nil {
			t.Fatalf("ExecuteTemplate: %v", err)
		}
		if !strings.Contains(buf.String(), `value="imap.example.com"`) {
			t.Errorf("account-edit-row fragment missing the host value, got:\n%s", buf.String())
		}
	})

	t.Run("test-result ok", func(t *testing.T) {
		var buf bytes.Buffer
		data := struct {
			OK          bool
			FolderCount int
			Error       string
		}{OK: true, FolderCount: 3}
		if err := accounts.ExecuteTemplate(&buf, "test-result", data); err != nil {
			t.Fatalf("ExecuteTemplate: %v", err)
		}
		if !strings.Contains(buf.String(), "3 folder(s)") {
			t.Errorf("test-result fragment missing the folder count, got:\n%s", buf.String())
		}
	})

	t.Run("test-result error", func(t *testing.T) {
		var buf bytes.Buffer
		data := struct {
			OK          bool
			FolderCount int
			Error       string
		}{Error: "connection refused"}
		if err := accounts.ExecuteTemplate(&buf, "test-result", data); err != nil {
			t.Fatalf("ExecuteTemplate: %v", err)
		}
		if !strings.Contains(buf.String(), "connection refused") {
			t.Errorf("test-result fragment missing the error, got:\n%s", buf.String())
		}
	})
}

func TestStaticFS_ContainsExpectedAssets(t *testing.T) {
	sub := StaticFS()
	for _, path := range []string{"css/app.css", "js/htmx.min.js", "js/app.js"} {
		if _, err := fs.Stat(sub, path); err != nil {
			t.Errorf("StaticFS() is missing %q: %v", path, err)
		}
	}
}
