package webui

import (
	"bytes"
	"html/template"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/yurydemin/marchi/internal/i18n"
)

// mustBind binds t's "T"/"conditionTypeLabel" to the English localizer —
// every test in this file asserts against English strings, so this is
// the test-local stand-in for what internal/httpapi's render() does with
// the request's actually-resolved language.
func mustBind(tb testing.TB, t *template.Template) *template.Template {
	tb.Helper()
	bound, err := Bind(t, i18n.NewLocalizer("en"))
	if err != nil {
		tb.Fatalf("Bind: %v", err)
	}
	return bound
}

// testStats mirrors the shape internal/httpapi's statsResponse feeds the
// "index" page with (Unlocked branch). Kept local rather than importing
// internal/httpapi, which itself imports this package.
type testStats struct {
	TotalEmails       int
	TotalAccounts     int
	ActiveAccounts    int
	LocalStorageBytes int64
	S3StorageBytes    int64
	S3Configured      bool
	S3Enabled         bool
	S3QueuePending    int
	S3QueueUploading  int
	S3QueueFailed     int
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
		{"locked", false, "Unlock Marchi", "Dashboard"},
		{"unlocked", true, "Dashboard", "Unlock Marchi"},
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
			if err := mustBind(t, index).ExecuteTemplate(&buf, "layout", data); err != nil {
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
		if err := mustBind(t, accounts).ExecuteTemplate(&buf, "layout", data); err != nil {
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
		if err := mustBind(t, accounts).ExecuteTemplate(&buf, "layout", data); err != nil {
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
		if err := mustBind(t, accounts).ExecuteTemplate(&buf, "account-rows", []testAccount{}); err != nil {
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
		if err := mustBind(t, accounts).ExecuteTemplate(&buf, "account-rows", data); err != nil {
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
		if err := mustBind(t, accounts).ExecuteTemplate(&buf, "account-row", a); err != nil {
			t.Fatalf("ExecuteTemplate: %v", err)
		}
		if !strings.Contains(buf.String(), "row@example.com") {
			t.Errorf("account-row fragment missing the email, got:\n%s", buf.String())
		}
	})

	t.Run("account-edit-row", func(t *testing.T) {
		var buf bytes.Buffer
		if err := mustBind(t, accounts).ExecuteTemplate(&buf, "account-edit-row", a); err != nil {
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
		if err := mustBind(t, accounts).ExecuteTemplate(&buf, "test-result", data); err != nil {
			t.Fatalf("ExecuteTemplate: %v", err)
		}
		if !strings.Contains(buf.String(), "3 folders") {
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
		if err := mustBind(t, accounts).ExecuteTemplate(&buf, "test-result", data); err != nil {
			t.Fatalf("ExecuteTemplate: %v", err)
		}
		if !strings.Contains(buf.String(), "connection refused") {
			t.Errorf("test-result fragment missing the error, got:\n%s", buf.String())
		}
	})
}

// testArchiveAccount/testArchiveFolder/testArchiveResult/testArchiveViewer
// mirror the exported fields internal/httpapi's archive_ui.go feeds the
// "archive" page with.
type testArchiveAccount struct {
	ID      int64
	Email   string
	Folders []testArchiveFolder
}

type testArchiveFolder struct {
	ID   int64
	Name string
	URL  string
}

type testArchiveResult struct {
	EmailID        int64
	Subject        string
	From           string
	Date           time.Time
	Size           int64
	HasAttachments bool
	InS3           bool
	AccountEmail   string
	FolderName     string
	ViewURL        string
	Selected       bool
}

type testArchiveAttachment struct {
	ID          int64
	Filename    string
	MIMEType    string
	Size        int64
	DownloadURL string
}

type testArchiveViewer struct {
	EmailID     int64
	Subject     string
	From        string
	To          []string
	Cc          []string
	Date        time.Time
	InS3        bool
	BodyHTML    template.HTML
	BodyText    string
	Attachments []testArchiveAttachment
	DownloadURL string
	PrevURL     string
	NextURL     string
	RestoreLogs []testArchiveRestoreLog
}

type testArchiveRestoreLog struct {
	ID                 int64
	TargetAccountEmail string
	TargetFolder       string
	Method             string
	Status             string
	ErrorMsg           string
	CreatedAt          time.Time
}

func TestParse_ReturnsArchivePage(t *testing.T) {
	pages, err := Parse()
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if _, ok := pages["archive"]; !ok {
		t.Fatal(`Parse() result is missing the "archive" page`)
	}
}

func TestParse_ArchivePage_RendersEmptyResults(t *testing.T) {
	pages, err := Parse()
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	archive := pages["archive"]

	data := struct {
		Unlocked                                                         bool
		Accounts                                                         []testArchiveAccount
		Query, Sender, Recipient, DateFrom, DateTo, HasAttachments, Sort string
		AccountID, FolderID                                              int64
		Results                                                          []testArchiveResult
		Total                                                            uint64
		Offset, Limit                                                    int
		PrevPageURL, NextPageURL                                         string
		Viewer                                                           *testArchiveViewer
	}{Unlocked: true, Sort: "relevance"}

	var buf bytes.Buffer
	if err := mustBind(t, archive).ExecuteTemplate(&buf, "layout", data); err != nil {
		t.Fatalf("ExecuteTemplate: %v", err)
	}
	if !strings.Contains(buf.String(), "No emails match this search") {
		t.Errorf("empty results must show the no-results message, got:\n%s", buf.String())
	}
}

func TestParse_ArchivePage_RendersTreeResultsAndViewer(t *testing.T) {
	pages, err := Parse()
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	archive := pages["archive"]

	when := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	data := struct {
		Unlocked                                                         bool
		Accounts                                                         []testArchiveAccount
		Query, Sender, Recipient, DateFrom, DateTo, HasAttachments, Sort string
		AccountID, FolderID                                              int64
		Results                                                          []testArchiveResult
		Total                                                            uint64
		Offset, Limit                                                    int
		PrevPageURL, NextPageURL                                         string
		Viewer                                                           *testArchiveViewer
	}{
		Unlocked: true, Sort: "relevance",
		Accounts: []testArchiveAccount{
			{ID: 1, Email: "a@example.com", Folders: []testArchiveFolder{{ID: 10, Name: "INBOX", URL: "/archive?account_id=1&folder_id=10"}}},
		},
		Results: []testArchiveResult{
			{EmailID: 100, Subject: "Hello", From: "sender@example.com", Date: when, Size: 2048, ViewURL: "/archive?view=100", Selected: true, InS3: true},
		},
		Total: 1, Offset: 0, Limit: 20,
		Viewer: &testArchiveViewer{
			EmailID: 100, Subject: "Hello", From: "sender@example.com", Date: when, InS3: true,
			BodyHTML: template.HTML("<p>Hi there</p>"), DownloadURL: "/api/v1/emails/100/download",
			Attachments: []testArchiveAttachment{{ID: 5, Filename: "file.pdf", Size: 1024, DownloadURL: "/api/v1/emails/100/attachments/5/download"}},
			RestoreLogs: []testArchiveRestoreLog{
				{ID: 1, TargetAccountEmail: "b@example.com", TargetFolder: "INBOX", Method: "imap_append", Status: "completed", CreatedAt: when},
			},
		},
	}

	var buf bytes.Buffer
	if err := mustBind(t, archive).ExecuteTemplate(&buf, "layout", data); err != nil {
		t.Fatalf("ExecuteTemplate: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"INBOX", "Hello", "sender@example.com", "<p>Hi there</p>", "file.pdf", "b@example.com", "imap_append", ">S3<"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered output missing %q, got:\n%s", want, out)
		}
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
