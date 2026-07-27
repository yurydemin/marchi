package s3store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/yurydemin/marchi/internal/db"
	"github.com/yurydemin/marchi/internal/db/repo"
	"github.com/yurydemin/marchi/internal/db/writer"
	"github.com/yurydemin/marchi/internal/domain"
	"github.com/yurydemin/marchi/internal/notify"
)

type recordingNotifier struct {
	events []notify.Event
}

func (r *recordingNotifier) Notify(ctx context.Context, e notify.Event) error {
	r.events = append(r.events, e)
	return nil
}

// newBacklogTestEnv sets up just enough state for checkBacklog: a real
// emails row (s3_upload_queue.email_id's FK target) and count queue rows
// pointing at it, all in "pending" status — checkBacklog never touches
// the S3 client or MasterKey, so neither is needed here.
func newBacklogTestEnv(t *testing.T, count int) *repo.S3UploadQueueRepo {
	t.Helper()
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "marchi.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	w := writer.New(sqlDB)
	t.Cleanup(func() { w.Close() })

	accountsRepo := repo.NewAccountsRepo(sqlDB, w)
	foldersRepo := repo.NewFoldersRepo(sqlDB, w)
	emailsRepo := repo.NewEmailsRepo(sqlDB, w)
	queueRepo := repo.NewS3UploadQueueRepo(sqlDB, w)
	ctx := context.Background()

	accountID, err := accountsRepo.Create(ctx, &domain.Account{
		Email: "backlog-test@example.com", IMAPHost: "imap.example.com", IMAPPort: 993,
		IMAPTLS: domain.IMAPTLSSSL, IMAPPasswordEncrypted: []byte("ct"),
	})
	if err != nil {
		t.Fatalf("creating account: %v", err)
	}
	folder, err := foldersRepo.UpsertFolder(ctx, accountID, "INBOX", 1)
	if err != nil {
		t.Fatalf("UpsertFolder: %v", err)
	}

	var emailID int64
	err = w.Do(ctx, func(tx *sql.Tx) error {
		var err error
		emailID, err = emailsRepo.Insert(ctx, tx, &domain.Email{
			MessageID: "backlog-test@example.com", AccountID: accountID, FolderID: folder.ID, UID: 1,
			StorageLocation: "local", LocalPath: "/tmp/does-not-matter.eml",
		})
		if err != nil {
			return err
		}
		for i := 0; i < count; i++ {
			if err := queueRepo.Enqueue(ctx, tx, emailID, fmt.Sprintf("key-%d", i), "/tmp/does-not-matter.eml"); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seeding email + queue rows: %v", err)
	}
	return queueRepo
}

func TestUploader_CheckBacklog_FiresWhenAtThreshold(t *testing.T) {
	queueRepo := newBacklogTestEnv(t, queueBacklogThreshold)
	n := &recordingNotifier{}
	u := NewUploader(UploaderDeps{QueueRepo: queueRepo, Notifier: n})

	u.checkBacklog(context.Background())

	if len(n.events) != 1 || n.events[0].Kind != "s3_queue_backlog" {
		t.Fatalf("events = %+v, want exactly one s3_queue_backlog event", n.events)
	}
}

func TestUploader_CheckBacklog_DoesNotFireBelowThreshold(t *testing.T) {
	queueRepo := newBacklogTestEnv(t, queueBacklogThreshold-1)
	n := &recordingNotifier{}
	u := NewUploader(UploaderDeps{QueueRepo: queueRepo, Notifier: n})

	u.checkBacklog(context.Background())

	if len(n.events) != 0 {
		t.Fatalf("events = %+v, want none below the threshold", n.events)
	}
}

func TestUploader_CheckBacklog_NoNotifierConfigured_SkipsTheQuery(t *testing.T) {
	// count doesn't matter — the point is CountByStatus must never even
	// be called when there's no Notifier to report to.
	u := NewUploader(UploaderDeps{QueueRepo: nil, Notifier: nil})
	u.checkBacklog(context.Background()) // must not panic on a nil QueueRepo
}
