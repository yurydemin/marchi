package sync

import (
	"context"
	"database/sql"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/client"

	"github.com/yurydemin/marchi/internal/db"
	"github.com/yurydemin/marchi/internal/db/repo"
	"github.com/yurydemin/marchi/internal/db/writer"
	"github.com/yurydemin/marchi/internal/domain"
	"github.com/yurydemin/marchi/internal/imapclient"
	"github.com/yurydemin/marchi/internal/maildir"
)

type fetchTestEnv struct {
	sqlDB        *sql.DB
	w            writer.Writer
	foldersR     *repo.FoldersRepo
	emailsR      *repo.EmailsRepo
	attachmentsR *repo.AttachmentsRepo
	accountID    int64
	maildirRoot  string
}

func newFetchTestEnv(t *testing.T) *fetchTestEnv {
	t.Helper()
	dataDir := t.TempDir()
	sqlDB, err := db.Open(filepath.Join(dataDir, "mailvault.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	w := writer.New(sqlDB)
	t.Cleanup(func() { w.Close() })

	accountsR := repo.NewAccountsRepo(sqlDB, w)
	accountID, err := accountsR.Create(context.Background(), &domain.Account{
		Email: "user@example.com", IMAPHost: "127.0.0.1", IMAPPort: 143, IsActive: true,
	})
	if err != nil {
		t.Fatalf("creating account fixture: %v", err)
	}

	return &fetchTestEnv{
		sqlDB:        sqlDB,
		w:            w,
		foldersR:     repo.NewFoldersRepo(sqlDB, w),
		emailsR:      repo.NewEmailsRepo(sqlDB, w),
		attachmentsR: repo.NewAttachmentsRepo(sqlDB, w),
		accountID:    accountID,
		maildirRoot:  filepath.Join(dataDir, "maildir"),
	}
}

func (env *fetchTestEnv) newWriter(t *testing.T, folderName string) *maildir.Writer {
	t.Helper()
	dir := maildir.FolderDir(env.maildirRoot, env.accountID, maildir.SafeFolderName(folderName))
	layout, err := maildir.NewLayout(dir)
	if err != nil {
		t.Fatalf("NewLayout: %v", err)
	}
	return maildir.NewWriter(layout, "test-host")
}

func connectToFakeServer(t *testing.T, addr string) *client.Client {
	t.Helper()
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}
	c, err := imapclient.Connect(context.Background(), imapclient.ConnectOptions{
		Host: host, Port: port, TLS: domain.IMAPTLSNone,
		Username: "user", Password: "pass", DialTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	return c
}

func testEmail(subject string) []byte {
	return []byte("Message-Id: <" + subject + "@example.com>\r\n" +
		"Subject: " + subject + "\r\n" +
		"From: sender@example.com\r\n" +
		"To: recipient@example.com\r\n" +
		"Date: Mon, 2 Jan 2006 15:04:05 +0000\r\n" +
		"\r\n" +
		"Body of " + subject + "\r\n")
}

func testEmailWithAttachment(subject, filename string) []byte {
	return []byte("Message-Id: <" + subject + "@example.com>\r\n" +
		"Subject: " + subject + "\r\n" +
		"From: sender@example.com\r\n" +
		"To: recipient@example.com\r\n" +
		"Date: Mon, 2 Jan 2006 15:04:05 +0000\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=\"BOUNDARY\"\r\n" +
		"\r\n" +
		"--BOUNDARY\r\n" +
		"Content-Type: text/plain\r\n" +
		"\r\n" +
		"Body of " + subject + "\r\n" +
		"--BOUNDARY\r\n" +
		"Content-Type: application/pdf\r\n" +
		"Content-Disposition: attachment; filename=\"" + filename + "\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n" +
		"\r\n" +
		"UERGLUZJTEUtQ09OVEVOVC1CWVRFUw==\r\n" +
		"--BOUNDARY--\r\n")
}

func TestFetchNewMessages_ArchivesEverythingAboveLastUID(t *testing.T) {
	env := newFetchTestEnv(t)

	addr := startFakeFetchServer(t, fakeFetchServer{
		uidValidity: 1001,
		uidNext:     4,
		messages: []fakeMessage{
			{uid: 1, flags: `\Seen`, body: testEmail("first")},
			{uid: 2, flags: `\Seen \Answered`, body: testEmail("second")},
			{uid: 3, flags: "", body: testEmail("third")},
		},
	})

	c := connectToFakeServer(t, addr)
	defer c.Logout()

	folder, err := env.foldersR.UpsertFolder(context.Background(), env.accountID, "INBOX", 1001)
	if err != nil {
		t.Fatalf("UpsertFolder: %v", err)
	}

	mw := env.newWriter(t, "INBOX")
	stats, err := FetchNewMessages(context.Background(), c, env.accountID, folder, mw, env.w, env.emailsR, env.foldersR, env.attachmentsR)
	if err != nil {
		t.Fatalf("FetchNewMessages: %v", err)
	}
	if stats.Archived != 3 {
		t.Fatalf("Archived = %d, want 3", stats.Archived)
	}
	if stats.Processed != 3 {
		t.Errorf("Processed = %d, want 3", stats.Processed)
	}
	if stats.Errors != 0 {
		t.Errorf("Errors = %d, want 0", stats.Errors)
	}
	if stats.Bytes == 0 {
		t.Error("Bytes should be non-zero (sum of archived message sizes)")
	}

	emails, err := env.emailsR.ListByFolder(context.Background(), folder.ID)
	if err != nil {
		t.Fatalf("ListByFolder: %v", err)
	}
	if len(emails) != 3 {
		t.Fatalf("got %d emails in DB, want 3", len(emails))
	}
	for i, e := range emails {
		wantUID := uint32(i + 1)
		if e.UID != wantUID {
			t.Errorf("emails[%d].UID = %d, want %d", i, e.UID, wantUID)
		}
		if e.StorageLocation != "local" {
			t.Errorf("StorageLocation = %q", e.StorageLocation)
		}
		data, err := os.ReadFile(e.LocalPath)
		if err != nil {
			t.Errorf("reading archived file %s: %v", e.LocalPath, err)
			continue
		}
		if len(data) == 0 {
			t.Errorf("archived file %s is empty", e.LocalPath)
		}
	}

	if e := emails[0]; e.Subject != "first" || e.FromAddr == "" || e.Date.IsZero() {
		t.Errorf("emails[0] metadata not extracted correctly: %+v", e)
	}

	// Message 2 has \Seen \Answered on the wire -> maildir flags "R","S",
	// sorted to "RS" in the filename (Writer's sortFlags, verified in the
	// maildir package's own tests — here just confirm it shows up).
	if !strings.Contains(filepath.Base(emails[1].LocalPath), ":2,RS") {
		t.Errorf("filename %s should end in :2,RS for \\Seen \\Answered", emails[1].LocalPath)
	}
	if !strings.Contains(filepath.Base(emails[2].LocalPath), ":2,") || strings.Contains(filepath.Base(emails[2].LocalPath), ":2,S") {
		t.Errorf("filename %s should have no flags (message 3 has none)", emails[2].LocalPath)
	}

	updatedFolder, err := env.foldersR.ListByAccount(context.Background(), env.accountID)
	if err != nil {
		t.Fatal(err)
	}
	if updatedFolder[0].LastUID != 3 {
		t.Errorf("LastUID = %d, want 3", updatedFolder[0].LastUID)
	}

	// The in-memory folder struct passed in must reflect the same last_uid
	// FetchNewMessages just wrote to the DB — a caller (e.g. the CLI's
	// summary printout) reading folder.LastUID right after the call
	// shouldn't see stale pre-fetch state.
	if folder.LastUID != 3 {
		t.Errorf("in-memory folder.LastUID = %d, want 3 (caller would print stale state)", folder.LastUID)
	}
}

func TestFetchNewMessages_NoNewMessages_IsANoOp(t *testing.T) {
	env := newFetchTestEnv(t)

	addr := startFakeFetchServer(t, fakeFetchServer{
		uidValidity: 1001,
		uidNext:     4, // highest UID is 3
		messages: []fakeMessage{
			{uid: 1, body: testEmail("x")},
			{uid: 2, body: testEmail("y")},
			{uid: 3, body: testEmail("z")},
		},
	})
	c := connectToFakeServer(t, addr)
	defer c.Logout()

	folder, err := env.foldersR.UpsertFolder(context.Background(), env.accountID, "INBOX", 1001)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.sqlDB.Exec(`UPDATE folders SET last_uid = 3 WHERE id = ?`, folder.ID); err != nil {
		t.Fatal(err)
	}
	folder.LastUID = 3

	mw := env.newWriter(t, "INBOX")
	stats, err := FetchNewMessages(context.Background(), c, env.accountID, folder, mw, env.w, env.emailsR, env.foldersR, env.attachmentsR)
	if err != nil {
		t.Fatalf("FetchNewMessages: %v", err)
	}
	if stats.Archived != 0 {
		t.Errorf("Archived = %d, want 0 (no new messages)", stats.Archived)
	}
}

func TestFetchNewMessages_SkipsWhenSyncDisabled(t *testing.T) {
	env := newFetchTestEnv(t)

	folder, err := env.foldersR.UpsertFolder(context.Background(), env.accountID, "INBOX", 1001)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.sqlDB.Exec(`UPDATE folders SET sync_enabled = 0 WHERE id = ?`, folder.ID); err != nil {
		t.Fatal(err)
	}
	folder.SyncEnabled = false

	mw := env.newWriter(t, "INBOX")
	// Deliberately nil client: FetchNewMessages must return before ever
	// touching it, since sync_enabled=false is checked first.
	stats, err := FetchNewMessages(context.Background(), nil, env.accountID, folder, mw, env.w, env.emailsR, env.foldersR, env.attachmentsR)
	if err != nil {
		t.Fatalf("FetchNewMessages: %v", err)
	}
	if stats.Archived != 0 {
		t.Errorf("Archived = %d, want 0", stats.Archived)
	}
}

func TestFetchNewMessages_ExtractsAttachments(t *testing.T) {
	env := newFetchTestEnv(t)

	addr := startFakeFetchServer(t, fakeFetchServer{
		uidValidity: 1001,
		uidNext:     3,
		messages: []fakeMessage{
			{uid: 1, body: testEmail("no attachment here")},
			{uid: 2, body: testEmailWithAttachment("has an attachment", "report.pdf")},
		},
	})
	c := connectToFakeServer(t, addr)
	defer c.Logout()

	folder, err := env.foldersR.UpsertFolder(context.Background(), env.accountID, "INBOX", 1001)
	if err != nil {
		t.Fatalf("UpsertFolder: %v", err)
	}

	mw := env.newWriter(t, "INBOX")
	stats, err := FetchNewMessages(context.Background(), c, env.accountID, folder, mw, env.w, env.emailsR, env.foldersR, env.attachmentsR)
	if err != nil {
		t.Fatalf("FetchNewMessages: %v", err)
	}
	if stats.Archived != 2 {
		t.Fatalf("Archived = %d, want 2", stats.Archived)
	}

	emails, err := env.emailsR.ListByFolder(context.Background(), folder.ID)
	if err != nil {
		t.Fatalf("ListByFolder: %v", err)
	}
	if len(emails) != 2 {
		t.Fatalf("got %d emails, want 2", len(emails))
	}

	if emails[0].HasAttachments {
		t.Error("emails[0] (no attachment) has_attachments should be false")
	}
	if !emails[1].HasAttachments {
		t.Error("emails[1] (with attachment) has_attachments should be true")
	}

	noneAttachments, err := env.attachmentsR.ListByEmail(context.Background(), emails[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(noneAttachments) != 0 {
		t.Errorf("emails[0] should have 0 attachment rows, got %d", len(noneAttachments))
	}

	withAttachments, err := env.attachmentsR.ListByEmail(context.Background(), emails[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(withAttachments) != 1 {
		t.Fatalf("emails[1] should have 1 attachment row, got %d", len(withAttachments))
	}
	att := withAttachments[0]
	if att.Filename != "report.pdf" {
		t.Errorf("Filename = %q", att.Filename)
	}
	if att.MIMEType != "application/pdf" {
		t.Errorf("MIMEType = %q", att.MIMEType)
	}
	if att.Size == 0 {
		t.Error("Size should be non-zero")
	}
}
