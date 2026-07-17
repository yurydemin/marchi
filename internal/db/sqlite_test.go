package db

import (
	"path/filepath"
	"testing"
)

var wantTables = []string{
	"accounts", "folders", "emails", "attachments",
	"rules", "s3_upload_queue", "sync_logs", "restore_logs",
}

func TestOpen_AppliesMigrations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mailvault.db")

	sqlDB, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer sqlDB.Close()

	for _, table := range wantTables {
		var name string
		err := sqlDB.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&name)
		if err != nil {
			t.Errorf("table %s: %v", table, err)
		}
	}
}

func TestOpen_IsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mailvault.db")

	db1, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	db1.Close()

	// Reopening an already-migrated database must not error (ErrNoChange
	// handled internally) — this is the path every subsequent CLI command
	// invocation takes.
	db2, err := Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer db2.Close()

	for _, table := range wantTables {
		var name string
		if err := db2.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&name); err != nil {
			t.Errorf("table %s missing after reopen: %v", table, err)
		}
	}
}

func TestOpen_PragmasApplied(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mailvault.db")
	sqlDB, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer sqlDB.Close()

	var journalMode string
	if err := sqlDB.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Errorf("journal_mode = %q, want wal", journalMode)
	}

	var foreignKeys int
	if err := sqlDB.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatalf("PRAGMA foreign_keys: %v", err)
	}
	if foreignKeys != 1 {
		t.Errorf("foreign_keys = %d, want 1", foreignKeys)
	}
}

func TestOpen_ForeignKeyCascadeWorks(t *testing.T) {
	// Regression guard for correction #11: foreign_keys must be ON on every
	// pooled connection, not just one, or cascade deletes silently leave
	// orphans depending on which connection serves the DELETE.
	path := filepath.Join(t.TempDir(), "mailvault.db")
	sqlDB, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer sqlDB.Close()
	sqlDB.SetMaxOpenConns(5) // force multiple pooled connections

	if _, err := sqlDB.Exec(`INSERT INTO accounts (email, imap_host) VALUES ('a@example.com', 'imap.example.com')`); err != nil {
		t.Fatalf("insert account: %v", err)
	}
	if _, err := sqlDB.Exec(`INSERT INTO folders (account_id, folder_name) VALUES (1, 'INBOX')`); err != nil {
		t.Fatalf("insert folder: %v", err)
	}

	for i := 0; i < 10; i++ {
		if _, err := sqlDB.Exec(`DELETE FROM accounts WHERE email = ?`, "nonexistent@example.com"); err != nil {
			t.Fatalf("warm up connection %d: %v", i, err)
		}
	}

	if _, err := sqlDB.Exec(`DELETE FROM accounts WHERE email = 'a@example.com'`); err != nil {
		t.Fatalf("delete account: %v", err)
	}

	var count int
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM folders WHERE account_id = 1`).Scan(&count); err != nil {
		t.Fatalf("count folders: %v", err)
	}
	if count != 0 {
		t.Errorf("expected cascade delete to remove the folder, %d remain", count)
	}
}

func TestOpen_RulesTableHasThreeRetentionColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mailvault.db")
	sqlDB, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer sqlDB.Close()

	rows, err := sqlDB.Query("PRAGMA table_info(rules)")
	if err != nil {
		t.Fatalf("PRAGMA table_info(rules): %v", err)
	}
	defer rows.Close()

	want := map[string]bool{
		"retention_local_days":      false,
		"retention_move_to_s3_days": false,
		"retention_s3_days":         false,
	}
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull, pk int
		var dfltValue any
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &pk); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if _, ok := want[name]; ok {
			want[name] = true
		}
	}
	for col, found := range want {
		if !found {
			t.Errorf("expected column %s on rules table, not found", col)
		}
	}
}

func TestClose_ChecksAndCloses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mailvault.db")
	sqlDB, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := sqlDB.Exec(`INSERT INTO accounts (email, imap_host) VALUES ('a@example.com', 'h')`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	if err := Close(sqlDB); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// The *sql.DB should genuinely be closed — further use must fail.
	if err := sqlDB.Ping(); err == nil {
		t.Error("expected Ping to fail after Close, got nil error")
	}

	// Reopening must see the data Close was supposed to have flushed.
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopening after Close: %v", err)
	}
	defer reopened.Close()
	var count int
	if err := reopened.QueryRow(`SELECT COUNT(*) FROM accounts`).Scan(&count); err != nil {
		t.Fatalf("counting accounts: %v", err)
	}
	if count != 1 {
		t.Errorf("got %d accounts after reopen, want 1 (Close should have flushed the WAL)", count)
	}
}
