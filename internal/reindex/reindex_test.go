package reindex

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/blugelabs/bluge"

	"github.com/yurydemin/marchi/internal/db"
	"github.com/yurydemin/marchi/internal/db/repo"
	"github.com/yurydemin/marchi/internal/db/writer"
	"github.com/yurydemin/marchi/internal/domain"
	"github.com/yurydemin/marchi/internal/search"
)

type testEnv struct {
	sqlDB     *sql.DB
	w         writer.Writer
	emailsR   *repo.EmailsRepo
	accountID int64
	folderID  int64
	dataDir   string
	indexPath string
}

func newTestEnv(t *testing.T) *testEnv {
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
		t.Fatalf("creating account: %v", err)
	}

	foldersR := repo.NewFoldersRepo(sqlDB, w)
	folder, err := foldersR.UpsertFolder(context.Background(), accountID, "INBOX", 1001)
	if err != nil {
		t.Fatalf("UpsertFolder: %v", err)
	}

	return &testEnv{
		sqlDB:     sqlDB,
		w:         w,
		emailsR:   repo.NewEmailsRepo(sqlDB, w),
		accountID: accountID,
		folderID:  folder.ID,
		dataDir:   dataDir,
		indexPath: filepath.Join(dataDir, "index"),
	}
}

// writeEmail writes an .eml file to disk and inserts a matching emails
// row pointing at it, returning the row's assigned ID.
func (env *testEnv) writeEmail(t *testing.T, uid uint32, raw []byte) int64 {
	t.Helper()
	path := filepath.Join(env.dataDir, fmt.Sprintf("msg-%d.eml", uid))
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var id int64
	err := env.w.Do(context.Background(), func(tx *sql.Tx) error {
		var err error
		id, err = env.emailsR.Insert(context.Background(), tx, &domain.Email{
			MessageID:       fmt.Sprintf("msg-%d@example.com", uid),
			AccountID:       env.accountID,
			FolderID:        env.folderID,
			UID:             uid,
			StorageLocation: "local",
			LocalPath:       path,
		})
		return err
	})
	if err != nil {
		t.Fatalf("inserting email: %v", err)
	}
	return id
}

func testMessage(subject string) []byte {
	return []byte("Message-Id: <" + subject + "@example.com>\r\n" +
		"Subject: " + subject + "\r\n" +
		"From: sender@example.com\r\n" +
		"To: recipient@example.com\r\n" +
		"Date: Mon, 2 Jan 2006 15:04:05 +0000\r\n\r\n" +
		"Body mentioning " + subject + ".\r\n")
}

func countMatches(t *testing.T, idx *search.Index, term, field string) int {
	t.Helper()
	reader, err := idx.Reader()
	if err != nil {
		t.Fatalf("Reader: %v", err)
	}
	defer reader.Close()

	iter, err := reader.Search(context.Background(), bluge.NewTopNSearch(10, bluge.NewMatchQuery(term).SetField(field)))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	n := 0
	for {
		m, err := iter.Next()
		if err != nil {
			t.Fatalf("iterating: %v", err)
		}
		if m == nil {
			break
		}
		n++
	}
	return n
}

func TestRun_IndexesAllLocalEmails(t *testing.T) {
	env := newTestEnv(t)
	env.writeEmail(t, 1, testMessage("firstmessage"))
	env.writeEmail(t, 2, testMessage("secondmessage"))

	idx, stats, err := Run(context.Background(), env.emailsR, env.indexPath)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	defer idx.Close()

	if stats != (Stats{Total: 2, Indexed: 2, Skipped: 0, Errors: 0}) {
		t.Errorf("stats = %+v, want {Total:2 Indexed:2 Skipped:0 Errors:0}", stats)
	}

	if n := countMatches(t, idx, "firstmessage", search.FieldBody); n != 1 {
		t.Errorf("body search for firstmessage: %d hits, want 1", n)
	}
	if n := countMatches(t, idx, "secondmessage", search.FieldBody); n != 1 {
		t.Errorf("body search for secondmessage: %d hits, want 1", n)
	}
}

func TestRun_WipesPreexistingIndexContent(t *testing.T) {
	env := newTestEnv(t)

	// Seed the index directory with a document that has no corresponding
	// emails row at all — simulating a stale/orphaned entry from before.
	preexisting, err := search.Open(env.indexPath)
	if err != nil {
		t.Fatalf("search.Open: %v", err)
	}
	if err := preexisting.Index(search.Doc{EmailID: 999, Subject: "orphandoc"}); err != nil {
		t.Fatalf("seeding orphan doc: %v", err)
	}
	if err := preexisting.Close(); err != nil {
		t.Fatalf("closing seed index: %v", err)
	}

	// Reindex against an emails table that knows nothing about that
	// orphan — FR-SR-04 requires the old index to be deleted, not merged
	// with the new content.
	env.writeEmail(t, 1, testMessage("realmessage"))

	idx, stats, err := Run(context.Background(), env.emailsR, env.indexPath)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	defer idx.Close()

	if stats.Total != 1 || stats.Indexed != 1 {
		t.Errorf("stats = %+v, want Total:1 Indexed:1", stats)
	}
	if n := countMatches(t, idx, "orphandoc", search.FieldSubject); n != 0 {
		t.Errorf("orphan document survived reindex: %d hits, want 0", n)
	}
	if n := countMatches(t, idx, "realmessage", search.FieldBody); n != 1 {
		t.Errorf("real document missing after reindex: %d hits, want 1", n)
	}
}

func TestRun_MissingLocalFile_CountsAsErrorNotAbort(t *testing.T) {
	env := newTestEnv(t)
	env.writeEmail(t, 1, testMessage("goodmessage"))

	// A row whose file was deleted out from under it (or never existed).
	err := env.w.Do(context.Background(), func(tx *sql.Tx) error {
		_, err := env.emailsR.Insert(context.Background(), tx, &domain.Email{
			MessageID:       "missing@example.com",
			AccountID:       env.accountID,
			FolderID:        env.folderID,
			UID:             2,
			StorageLocation: "local",
			LocalPath:       filepath.Join(env.dataDir, "does-not-exist.eml"),
		})
		return err
	})
	if err != nil {
		t.Fatalf("inserting email with missing file: %v", err)
	}

	idx, stats, err := Run(context.Background(), env.emailsR, env.indexPath)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	defer idx.Close()

	if stats.Total != 2 {
		t.Errorf("Total = %d, want 2", stats.Total)
	}
	if stats.Indexed != 1 {
		t.Errorf("Indexed = %d, want 1 (the good message)", stats.Indexed)
	}
	if stats.Errors != 1 {
		t.Errorf("Errors = %d, want 1 (the missing file)", stats.Errors)
	}
	if n := countMatches(t, idx, "goodmessage", search.FieldBody); n != 1 {
		t.Errorf("good message not indexed despite the other row's missing file: %d hits, want 1", n)
	}
}

func TestRun_SkipsNonLocalEmails(t *testing.T) {
	env := newTestEnv(t)
	err := env.w.Do(context.Background(), func(tx *sql.Tx) error {
		_, err := env.emailsR.Insert(context.Background(), tx, &domain.Email{
			MessageID:       "s3only@example.com",
			AccountID:       env.accountID,
			FolderID:        env.folderID,
			UID:             1,
			StorageLocation: "s3",
		})
		return err
	})
	if err != nil {
		t.Fatalf("inserting s3-only email: %v", err)
	}

	idx, stats, err := Run(context.Background(), env.emailsR, env.indexPath)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	defer idx.Close()

	if stats.Total != 1 || stats.Skipped != 1 || stats.Indexed != 0 || stats.Errors != 0 {
		t.Errorf("stats = %+v, want Total:1 Skipped:1 Indexed:0 Errors:0", stats)
	}
}
