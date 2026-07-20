package retention

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yurydemin/marchi/internal/db"
	"github.com/yurydemin/marchi/internal/db/repo"
	"github.com/yurydemin/marchi/internal/db/writer"
	"github.com/yurydemin/marchi/internal/domain"
)

type testEnv struct {
	sqlDB                 *sql.DB
	w                     writer.Writer
	accountsRepo          *repo.AccountsRepo
	foldersRepo           *repo.FoldersRepo
	emailsRepo            *repo.EmailsRepo
	retentionSettingsRepo *repo.RetentionSettingsRepo
	s3ConfigRepo          *repo.S3ConfigRepo
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "marchi.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	w := writer.New(sqlDB)
	t.Cleanup(func() { w.Close() })

	return &testEnv{
		sqlDB: sqlDB, w: w,
		accountsRepo:          repo.NewAccountsRepo(sqlDB, w),
		foldersRepo:           repo.NewFoldersRepo(sqlDB, w),
		emailsRepo:            repo.NewEmailsRepo(sqlDB, w),
		retentionSettingsRepo: repo.NewRetentionSettingsRepo(sqlDB, w),
		s3ConfigRepo:          repo.NewS3ConfigRepo(sqlDB, w),
	}
}

func (e *testEnv) createAccount(t *testing.T, email string, retentionLocalDays *int) int64 {
	t.Helper()
	id, err := e.accountsRepo.Create(context.Background(), &domain.Account{
		Email: email, IMAPHost: "imap.example.com", IMAPPort: 993, IMAPTLS: domain.IMAPTLSSSL,
		IMAPPasswordEncrypted: []byte("ct"), IsActive: true, RetentionLocalDays: retentionLocalDays,
	})
	if err != nil {
		t.Fatalf("creating account: %v", err)
	}
	return id
}

func (e *testEnv) createEmail(t *testing.T, accountID int64, uid uint32, createdAt time.Time) (int64, string) {
	t.Helper()
	ctx := context.Background()
	folder, err := e.foldersRepo.UpsertFolder(ctx, accountID, "INBOX", 1)
	if err != nil {
		t.Fatalf("UpsertFolder: %v", err)
	}

	localPath := filepath.Join(t.TempDir(), "msg.eml")
	if err := os.WriteFile(localPath, []byte("From: a@example.com\r\nSubject: test\r\n\r\nbody"), 0o644); err != nil {
		t.Fatalf("writing local .eml: %v", err)
	}

	var id int64
	err = e.w.Do(ctx, func(tx *sql.Tx) error {
		var err error
		id, err = e.emailsRepo.Insert(ctx, tx, &domain.Email{
			MessageID: "retention@example.com", AccountID: accountID, FolderID: folder.ID, UID: uid,
			StorageLocation: "local", LocalPath: localPath,
		})
		return err
	})
	if err != nil {
		t.Fatalf("inserting email: %v", err)
	}
	e.backdate(t, "created_at", id, createdAt)
	return id, localPath
}

func (e *testEnv) backdate(t *testing.T, column string, id int64, when time.Time) {
	t.Helper()
	if _, err := e.sqlDB.Exec(`UPDATE emails SET `+column+` = ? WHERE id = ?`, when.UTC().Format("2006-01-02 15:04:05"), id); err != nil {
		t.Fatalf("backdating %s: %v", column, err)
	}
}

func intPtr(n int) *int { return &n }

// TestRunner_S3NotConfigured_DirectDeletesPastRetentionLocalDays covers
// the "no S3 backup exists" path: an old-enough local email with no S3
// mirror gets deleted outright, both the DB row and the on-disk file.
func TestRunner_S3NotConfigured_DirectDeletesPastRetentionLocalDays(t *testing.T) {
	env := newTestEnv(t)
	accountID := env.createAccount(t, "user@example.com", intPtr(30))

	fixedNow := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	oldEnough := fixedNow.AddDate(0, 0, -31)
	tooRecent := fixedNow.AddDate(0, 0, -10)

	oldID, oldPath := env.createEmail(t, accountID, 1, oldEnough)
	recentID, recentPath := env.createEmail(t, accountID, 2, tooRecent)

	runner := New(Deps{
		AccountsRepo: env.accountsRepo, EmailsRepo: env.emailsRepo,
		RetentionSettingsRepo: env.retentionSettingsRepo, S3ConfigRepo: env.s3ConfigRepo,
		Writer: env.w, Now: func() time.Time { return fixedNow },
	})

	stats, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.DeletedDirect != 1 || stats.Errors != 0 {
		t.Fatalf("stats = %+v, want DeletedDirect=1 Errors=0", stats)
	}

	if _, err := env.emailsRepo.GetByID(context.Background(), oldID); err == nil {
		t.Error("old email still exists in SQLite, want deleted")
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Error("old email's local file still exists, want deleted")
	}

	if _, err := env.emailsRepo.GetByID(context.Background(), recentID); err != nil {
		t.Errorf("recent email was deleted, want kept: %v", err)
	}
	if _, err := os.Stat(recentPath); err != nil {
		t.Errorf("recent email's local file was deleted, want kept: %v", err)
	}
}

func TestRunner_NoPolicySet_NothingHappens(t *testing.T) {
	env := newTestEnv(t)
	accountID := env.createAccount(t, "user@example.com", nil) // no override, no global default either
	id, path := env.createEmail(t, accountID, 1, time.Now().AddDate(0, 0, -9999))

	runner := New(Deps{
		AccountsRepo: env.accountsRepo, EmailsRepo: env.emailsRepo,
		RetentionSettingsRepo: env.retentionSettingsRepo, S3ConfigRepo: env.s3ConfigRepo,
		Writer: env.w,
	})
	stats, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.DeletedDirect != 0 || stats.MovedToS3Only != 0 || stats.DeletedFromS3 != 0 {
		t.Errorf("stats = %+v, want all zero (no policy means keep forever)", stats)
	}
	if _, err := env.emailsRepo.GetByID(context.Background(), id); err != nil {
		t.Errorf("email was removed despite no retention policy: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("local file was removed despite no retention policy: %v", err)
	}
}

func TestRunner_GlobalDefault_AppliesWhenAccountHasNoOverride(t *testing.T) {
	env := newTestEnv(t)
	if err := env.retentionSettingsRepo.Upsert(context.Background(), &domain.RetentionSettings{DefaultLocalDays: intPtr(5)}); err != nil {
		t.Fatalf("Upsert global settings: %v", err)
	}
	accountID := env.createAccount(t, "user@example.com", nil)

	fixedNow := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	id, _ := env.createEmail(t, accountID, 1, fixedNow.AddDate(0, 0, -10))

	runner := New(Deps{
		AccountsRepo: env.accountsRepo, EmailsRepo: env.emailsRepo,
		RetentionSettingsRepo: env.retentionSettingsRepo, S3ConfigRepo: env.s3ConfigRepo,
		Writer: env.w, Now: func() time.Time { return fixedNow },
	})
	stats, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.DeletedDirect != 1 {
		t.Errorf("stats = %+v, want DeletedDirect=1 (global default should have applied)", stats)
	}
	if _, err := env.emailsRepo.GetByID(context.Background(), id); err == nil {
		t.Error("email still exists, want deleted via global default policy")
	}
}

func TestRunner_AccountOverride_WinsOverGlobalDefault(t *testing.T) {
	env := newTestEnv(t)
	// Global default would delete after 5 days...
	if err := env.retentionSettingsRepo.Upsert(context.Background(), &domain.RetentionSettings{DefaultLocalDays: intPtr(5)}); err != nil {
		t.Fatalf("Upsert global settings: %v", err)
	}
	// ...but this account overrides to 100 days, so a 10-day-old email must survive.
	accountID := env.createAccount(t, "user@example.com", intPtr(100))

	fixedNow := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	id, _ := env.createEmail(t, accountID, 1, fixedNow.AddDate(0, 0, -10))

	runner := New(Deps{
		AccountsRepo: env.accountsRepo, EmailsRepo: env.emailsRepo,
		RetentionSettingsRepo: env.retentionSettingsRepo, S3ConfigRepo: env.s3ConfigRepo,
		Writer: env.w, Now: func() time.Time { return fixedNow },
	})
	stats, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.DeletedDirect != 0 {
		t.Errorf("stats = %+v, want DeletedDirect=0 (account override should have protected this email)", stats)
	}
	if _, err := env.emailsRepo.GetByID(context.Background(), id); err != nil {
		t.Errorf("email was deleted despite account override, want kept: %v", err)
	}
}
