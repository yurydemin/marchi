package restore

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"

	"github.com/yurydemin/marchi/internal/account"
	"github.com/yurydemin/marchi/internal/db"
	"github.com/yurydemin/marchi/internal/db/repo"
	"github.com/yurydemin/marchi/internal/db/writer"
	"github.com/yurydemin/marchi/internal/domain"
	"github.com/yurydemin/marchi/internal/s3store"
	"github.com/yurydemin/marchi/internal/security/crypto"
	"github.com/yurydemin/marchi/internal/testutil/dovecot"
	"github.com/yurydemin/marchi/internal/testutil/minio"
)

type restoreTestEnv struct {
	sqlDB           *sql.DB
	w               writer.Writer
	accountsRepo    *repo.AccountsRepo
	foldersRepo     *repo.FoldersRepo
	emailsRepo      *repo.EmailsRepo
	restoreLogsRepo *repo.RestoreLogsRepo
	manager         *account.Manager
}

func newRestoreTestEnv(t *testing.T) *restoreTestEnv {
	t.Helper()
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "mailvault.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	w := writer.New(sqlDB)
	t.Cleanup(func() { w.Close() })

	accountsRepo := repo.NewAccountsRepo(sqlDB, w)
	masterKey := make([]byte, crypto.KeySize)
	if _, err := io.ReadFull(rand.Reader, masterKey); err != nil {
		t.Fatal(err)
	}
	mgr, err := account.NewManager(accountsRepo, masterKey)
	if err != nil {
		t.Fatalf("account.NewManager: %v", err)
	}

	return &restoreTestEnv{
		sqlDB: sqlDB, w: w,
		accountsRepo:    accountsRepo,
		foldersRepo:     repo.NewFoldersRepo(sqlDB, w),
		emailsRepo:      repo.NewEmailsRepo(sqlDB, w),
		restoreLogsRepo: repo.NewRestoreLogsRepo(sqlDB, w),
		manager:         mgr,
	}
}

// createTargetAccount registers srv as a plain-password IMAP account —
// the "target account" a restore delivers into.
func (e *restoreTestEnv) createTargetAccount(t *testing.T, srv *dovecot.Server, user, password string) int64 {
	t.Helper()
	a, err := e.manager.AddAccount(context.Background(), account.AddAccountParams{
		Email: user, IMAPHost: srv.Host, IMAPPort: srv.Port, IMAPTLS: domain.IMAPTLSNone,
		IMAPUsername: user, IMAPPassword: password,
	})
	if err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	return a.ID
}

// seedLocalEmail inserts an emails row with storage_location=local,
// writing raw to a real temp file so os.ReadFile in loadContent works.
func (e *restoreTestEnv) seedLocalEmail(t *testing.T, sourceAccountID int64, raw []byte, flags []string) int64 {
	t.Helper()
	ctx := context.Background()
	folder, err := e.foldersRepo.UpsertFolder(ctx, sourceAccountID, "INBOX", 1)
	if err != nil {
		t.Fatalf("UpsertFolder: %v", err)
	}
	localPath := filepath.Join(t.TempDir(), "seed.eml")
	if err := os.WriteFile(localPath, raw, 0o644); err != nil {
		t.Fatalf("writing seed .eml: %v", err)
	}

	var id int64
	err = e.w.Do(ctx, func(tx *sql.Tx) error {
		var err error
		id, err = e.emailsRepo.Insert(ctx, tx, &domain.Email{
			MessageID: "restore-source@example.com", AccountID: sourceAccountID, FolderID: folder.ID, UID: 1,
			StorageLocation: "local", LocalPath: localPath, Flags: flags,
			Date: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC),
		})
		return err
	})
	if err != nil {
		t.Fatalf("inserting seed email: %v", err)
	}
	return id
}

// TestRestoreOne_LocalEmail_AppendsIntoRealDovecot is Phase 3 step 11's
// demo criterion (local half): restore a locally-stored email via IMAP
// APPEND into a real Dovecot target mailbox, then confirm via IMAP FETCH
// that the content and flags actually landed.
func TestRestoreOne_LocalEmail_AppendsIntoRealDovecot(t *testing.T) {
	env := newRestoreTestEnv(t)
	srv := dovecot.Start(t, "restoretarget@dovecot.local", "restorepass123")
	targetAccountID := env.createTargetAccount(t, srv, "restoretarget@dovecot.local", "restorepass123")

	raw := []byte("From: sender@example.com\r\nSubject: restore me\r\nMessage-ID: <local-restore@example.com>\r\n\r\nBody of the restored message.\r\n")
	emailID := env.seedLocalEmail(t, targetAccountID, raw, []string{imap.SeenFlag})

	restorer := New(Deps{
		EmailsRepo: env.emailsRepo, AccountsRepo: env.accountsRepo,
		RestoreLogsRepo: env.restoreLogsRepo, Manager: env.manager,
	})

	log, err := restorer.RestoreOne(context.Background(), emailID, targetAccountID, "INBOX")
	if err != nil {
		t.Fatalf("RestoreOne: %v", err)
	}
	if log.Status != domain.RestoreStatusCompleted || log.Method != domain.RestoreMethodIMAPAppend {
		t.Fatalf("log = %+v, want completed/imap_append", log)
	}

	subject, flags := fetchRestoredMessage(t, srv, "restoretarget@dovecot.local", "restorepass123", "local-restore@example.com")
	if subject != "restore me" {
		t.Errorf("restored Subject = %q, want %q", subject, "restore me")
	}
	if !containsFlag(flags, imap.SeenFlag) {
		t.Errorf("restored flags = %v, want \\Seen preserved from archival", flags)
	}
}

// TestRestoreOne_S3ResidentEmail_LazyLoadsThenAppendsIntoRealDovecot is
// step 11's demo criterion (S3 half): an email evicted to S3-only
// (storage_location=s3, no local copy) gets lazy-loaded and decrypted
// (FR-RS-03) before being restored the same way.
func TestRestoreOne_S3ResidentEmail_LazyLoadsThenAppendsIntoRealDovecot(t *testing.T) {
	env := newRestoreTestEnv(t)
	srv := dovecot.Start(t, "s3restoretarget@dovecot.local", "restorepass123")
	targetAccountID := env.createTargetAccount(t, srv, "s3restoretarget@dovecot.local", "restorepass123")

	minioSrv := minio.Start(t)
	s3Client, err := s3store.NewClient(s3store.Options{
		Endpoint: minioSrv.Endpoint, Region: "us-east-1", Bucket: "mailvault-restore-test",
		AccessKeyID: minioSrv.AccessKeyID, SecretAccessKey: minioSrv.SecretKey, PathStyle: true,
	})
	if err != nil {
		t.Fatalf("s3store.NewClient: %v", err)
	}
	if err := s3Client.EnsureBucket(context.Background()); err != nil {
		t.Fatalf("EnsureBucket: %v", err)
	}

	masterKey := make([]byte, crypto.KeySize)
	if _, err := io.ReadFull(rand.Reader, masterKey); err != nil {
		t.Fatal(err)
	}
	cache, err := s3store.NewCache(filepath.Join(t.TempDir(), "cache"), 10*1024*1024)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	lazyLoader := &s3store.LazyLoader{Client: s3Client, Cache: cache, MasterKey: masterKey}

	raw := []byte("From: sender@example.com\r\nSubject: restore me from s3\r\nMessage-ID: <s3-restore@example.com>\r\n\r\nBody restored from S3.\r\n")
	s3Key := s3store.EmailKey(targetAccountID, time.Now().UTC(), s3store.ContentSHA256Hex(raw))
	body, metadata, err := s3store.EncryptObject(masterKey, raw)
	if err != nil {
		t.Fatalf("EncryptObject: %v", err)
	}
	if _, err := s3Client.Put(context.Background(), s3Key, bytes.NewReader(body), metadata); err != nil {
		t.Fatalf("Put: %v", err)
	}

	ctx := context.Background()
	folder, err := env.foldersRepo.UpsertFolder(ctx, targetAccountID, "INBOX", 1)
	if err != nil {
		t.Fatalf("UpsertFolder: %v", err)
	}
	var emailID int64
	err = env.w.Do(ctx, func(tx *sql.Tx) error {
		var err error
		emailID, err = env.emailsRepo.Insert(ctx, tx, &domain.Email{
			MessageID: "restore-source-s3@example.com", AccountID: targetAccountID, FolderID: folder.ID, UID: 1,
			StorageLocation: "s3", Date: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC),
		})
		return err
	})
	if err != nil {
		t.Fatalf("inserting seed email: %v", err)
	}
	if _, err := env.sqlDB.Exec(`UPDATE emails SET s3_key = ? WHERE id = ?`, s3Key, emailID); err != nil {
		t.Fatalf("recording s3_key: %v", err)
	}

	restorer := New(Deps{
		EmailsRepo: env.emailsRepo, AccountsRepo: env.accountsRepo,
		RestoreLogsRepo: env.restoreLogsRepo, Manager: env.manager, LazyLoader: lazyLoader,
	})

	log, err := restorer.RestoreOne(ctx, emailID, targetAccountID, "INBOX")
	if err != nil {
		t.Fatalf("RestoreOne: %v", err)
	}
	if log.Status != domain.RestoreStatusCompleted || log.Method != domain.RestoreMethodIMAPAppend {
		t.Fatalf("log = %+v, want completed/imap_append", log)
	}

	subject, _ := fetchRestoredMessage(t, srv, "s3restoretarget@dovecot.local", "restorepass123", "s3-restore@example.com")
	if subject != "restore me from s3" {
		t.Errorf("restored Subject = %q, want %q", subject, "restore me from s3")
	}
}

// fetchRestoredMessage connects directly (bypassing this project's own
// restore/sync code, so the test is verifying against an independent
// client) to srv, SELECTs INBOX, and FETCHes the message whose Message-ID
// contains messageIDFragment, returning its Subject and flags.
func fetchRestoredMessage(t *testing.T, srv *dovecot.Server, user, password, messageIDFragment string) (subject string, flags []string) {
	t.Helper()
	c, err := client.Dial(fmt.Sprintf("%s:%d", srv.Host, srv.Port))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Logout()
	if err := c.Login(user, password); err != nil {
		t.Fatalf("login: %v", err)
	}
	if _, err := c.Select("INBOX", false); err != nil {
		t.Fatalf("SELECT INBOX: %v", err)
	}

	seqset := new(imap.SeqSet)
	seqset.AddRange(1, 0) // 1:* — every message
	messages := make(chan *imap.Message, 10)
	done := make(chan error, 1)
	go func() {
		done <- c.Fetch(seqset, []imap.FetchItem{imap.FetchEnvelope, imap.FetchFlags}, messages)
	}()

	var found *imap.Message
	for msg := range messages {
		if msg.Envelope != nil && containsSubstring(msg.Envelope.MessageId, messageIDFragment) {
			found = msg
		}
	}
	if err := <-done; err != nil {
		t.Fatalf("FETCH: %v", err)
	}
	if found == nil {
		t.Fatalf("no message with Message-ID containing %q found in target mailbox", messageIDFragment)
	}
	return found.Envelope.Subject, found.Flags
}

func containsSubstring(s, substr string) bool {
	return len(s) >= len(substr) && (func() bool {
		for i := 0; i+len(substr) <= len(s); i++ {
			if s[i:i+len(substr)] == substr {
				return true
			}
		}
		return false
	})()
}

func containsFlag(flags []string, want string) bool {
	for _, f := range flags {
		if f == want {
			return true
		}
	}
	return false
}
