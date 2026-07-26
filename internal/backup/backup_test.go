package backup

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/yurydemin/marchi/internal/db"
)

func TestRun_ProducesVerifiableBackupWithData(t *testing.T) {
	dataDir := t.TempDir()
	maildirRoot := filepath.Join(dataDir, "maildir")
	if err := os.MkdirAll(filepath.Join(maildirRoot, "accounts", "1", "INBOX", "cur"), 0o755); err != nil {
		t.Fatalf("seeding maildir fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(maildirRoot, "accounts", "1", "INBOX", "cur", "123.eml"), []byte("Subject: hi\r\n\r\nbody\r\n"), 0o644); err != nil {
		t.Fatalf("seeding maildir fixture: %v", err)
	}

	sqlDB, err := db.Open(filepath.Join(dataDir, "marchi.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer sqlDB.Close()
	if _, err := sqlDB.Exec(`INSERT INTO accounts (email, imap_host, imap_port, imap_tls, imap_password_encrypted, is_active) VALUES (?, ?, ?, ?, ?, ?)`,
		"backup-test@example.com", "imap.example.com", 993, "ssl", []byte("ciphertext"), true); err != nil {
		t.Fatalf("seeding accounts row: %v", err)
	}

	// Master Key material — only .salt is realistic to fabricate here
	// without going through the real masterkey package, but that's
	// enough to prove Run copies whatever key files exist.
	if err := os.WriteFile(filepath.Join(dataDir, ".salt"), []byte("fake-salt-bytes"), 0o600); err != nil {
		t.Fatalf("seeding .salt fixture: %v", err)
	}

	destDir := filepath.Join(t.TempDir(), "backup-1")
	manifest, err := Run(context.Background(), sqlDB, dataDir, maildirRoot, destDir)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	wantNames := map[string]bool{dbFilename: false, maildirArchive: false, saltFilename: false}
	for _, f := range manifest.Files {
		if _, ok := wantNames[f.Name]; ok {
			wantNames[f.Name] = true
		}
		if f.SHA256 == "" {
			t.Errorf("file %s has an empty SHA-256", f.Name)
		}
		if f.Size == 0 {
			t.Errorf("file %s has size 0", f.Name)
		}
	}
	for name, found := range wantNames {
		if !found {
			t.Errorf("manifest is missing expected file %q", name)
		}
	}
	// .mk-verify and .dek were never created in this fixture — Run must
	// not have failed over their absence, and must not have listed them.
	for _, f := range manifest.Files {
		if f.Name == verifyFilename || f.Name == dekFilename {
			t.Errorf("manifest lists %s, which was never created", f.Name)
		}
	}

	// The backed-up database must actually contain the row inserted
	// above — proof the Online Backup API copied real data, not just an
	// empty schema.
	backupDB, err := sql.Open("sqlite", "file:"+filepath.Join(destDir, dbFilename)+"?mode=ro")
	if err != nil {
		t.Fatalf("opening backed-up database: %v", err)
	}
	defer backupDB.Close()
	var email string
	if err := backupDB.QueryRow(`SELECT email FROM accounts WHERE id = 1`).Scan(&email); err != nil {
		t.Fatalf("querying backed-up database: %v", err)
	}
	if email != "backup-test@example.com" {
		t.Errorf("backed-up accounts.email = %q, want the seeded row", email)
	}

	result, err := Verify(destDir)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !result.OK {
		t.Errorf("Verify result not OK: mismatched=%v integrity=%q", result.MismatchedFiles, result.IntegrityCheckMsg)
	}
	if result.IntegrityCheckMsg != "ok" {
		t.Errorf("IntegrityCheckMsg = %q, want %q", result.IntegrityCheckMsg, "ok")
	}
}

func TestRun_RefusesNonEmptyDestination(t *testing.T) {
	dataDir := t.TempDir()
	sqlDB, err := db.Open(filepath.Join(dataDir, "marchi.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer sqlDB.Close()

	destDir := t.TempDir() // t.TempDir() itself already exists and, for this test, stays empty until we add a file
	if err := os.WriteFile(filepath.Join(destDir, "already-here.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("seeding pre-existing file: %v", err)
	}

	if _, err := Run(context.Background(), sqlDB, dataDir, filepath.Join(dataDir, "maildir"), destDir); err == nil {
		t.Error("Run succeeded against a non-empty destination, want an error")
	}
}

func TestRun_MaildirRootMissing_ProducesEmptyArchiveNotError(t *testing.T) {
	dataDir := t.TempDir()
	sqlDB, err := db.Open(filepath.Join(dataDir, "marchi.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer sqlDB.Close()

	destDir := filepath.Join(t.TempDir(), "backup-1")
	if _, err := Run(context.Background(), sqlDB, dataDir, filepath.Join(dataDir, "never-existed"), destDir); err != nil {
		t.Fatalf("Run with a missing maildir root: %v", err)
	}
}

func TestVerify_DetectsCorruption(t *testing.T) {
	dataDir := t.TempDir()
	sqlDB, err := db.Open(filepath.Join(dataDir, "marchi.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer sqlDB.Close()

	destDir := filepath.Join(t.TempDir(), "backup-1")
	if _, err := Run(context.Background(), sqlDB, dataDir, filepath.Join(dataDir, "maildir"), destDir); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Corrupt the maildir archive after the fact — simulates bit rot or
	// a truncated copy to another disk.
	if err := os.WriteFile(filepath.Join(destDir, maildirArchive), []byte("corrupted"), 0o644); err != nil {
		t.Fatalf("corrupting backup: %v", err)
	}

	result, err := Verify(destDir)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if result.OK {
		t.Error("Verify reported OK on a corrupted backup")
	}
	found := false
	for _, name := range result.MismatchedFiles {
		if name == maildirArchive {
			found = true
		}
	}
	if !found {
		t.Errorf("MismatchedFiles = %v, want it to include %q", result.MismatchedFiles, maildirArchive)
	}
}
