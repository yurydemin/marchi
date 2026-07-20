package main

import (
	"bytes"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/yurydemin/marchi/internal/testutil/dovecot"
)

// TestCrashRecovery_KillMidSync_ResumesWithoutDuplicatesOrLoss is this
// phase's final acceptance test (NFR-RL-02/03): SIGKILL the real compiled
// marchi binary partway through a sync — a hard crash with no chance to
// run any cleanup at all, unlike the graceful SIGINT/SIGTERM path already
// covered by internal/sync's own cancellation tests — then restart it and
// confirm the archive ends up complete, with no duplicate or missing
// messages and no database corruption.
func TestCrashRecovery_KillMidSync_ResumesWithoutDuplicatesOrLoss(t *testing.T) {
	binary := buildMarchi(t)
	srv := dovecot.Start(t, "testuser@dovecot.local", "testpass123")

	const (
		totalMessages = 60
		masterKey     = "correct horse battery staple"
		email         = "testuser@dovecot.local"
	)
	for i := 0; i < totalMessages; i++ {
		srv.AppendMessage(t, email, "testpass123", "INBOX", testMessage(i))
	}

	dataDir := t.TempDir()
	runMarchi(t, binary, dataDir, nil, masterKey+"\n"+masterKey+"\n", "unlock")
	runMarchi(t, binary, dataDir, nil, masterKey+"\ntestpass123\n",
		"add-account", email, "--host", srv.Host, "--port", fmt.Sprint(srv.Port), "--tls", "none")

	killMidSync(t, binary, dataDir, masterKey, email)

	dbPath := filepath.Join(dataDir, "data", "marchi.db")
	partial := countEmails(t, dbPath)
	if partial == 0 {
		t.Fatal("expected at least some messages archived before the kill, got 0 — timing needs adjusting")
	}
	if partial >= totalMessages {
		t.Fatalf("got %d archived before the kill, want < %d — sync finished before SIGKILL landed, timing needs adjusting", partial, totalMessages)
	}
	assertNoDuplicateUIDs(t, dbPath)
	assertLocalPathsExist(t, dbPath, dataDir)
	t.Logf("archived %d/%d messages before SIGKILL", partial, totalMessages)

	// Resume: a fresh process, same data dir.
	out := runMarchi(t, binary, dataDir, nil, masterKey+"\n", "sync", email)
	t.Logf("resume sync output:\n%s", out)

	final := countEmails(t, dbPath)
	if final != totalMessages {
		t.Errorf("got %d emails after resuming, want %d", final, totalMessages)
	}
	assertNoDuplicateUIDs(t, dbPath)
	assertNoGapsInUIDs(t, dbPath, totalMessages)
	assertLocalPathsExist(t, dbPath, dataDir)
	assertTmpDirEmpty(t, dataDir)
}

func testMessage(i int) []byte {
	return []byte(fmt.Sprintf(
		"Message-Id: <crashtest-%d@example.com>\r\n"+
			"Subject: Crash recovery test message %d\r\n"+
			"From: sender@example.com\r\n"+
			"To: testuser@dovecot.local\r\n"+
			"Date: Mon, 2 Jan 2006 15:04:05 +0000\r\n\r\n"+
			"Body of message %d. %s\r\n",
		i, i, i, strings.Repeat("padding ", 200)))
}

// buildMarchi compiles the real CLI binary once per test run — the
// point of this test is exercising the actual process lifecycle
// (SIGKILL, restart, on-disk recovery), not internal Go function calls.
func buildMarchi(t *testing.T) string {
	t.Helper()
	binPath := filepath.Join(t.TempDir(), "marchi")
	cmd := exec.Command("go", "build", "-o", binPath, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building marchi: %v\n%s", err, out)
	}
	return binPath
}

// runMarchi runs the compiled binary with args in dataDir (its working
// directory — the zero-config default paths, e.g. "./data/marchi.db",
// then resolve inside the test's isolated temp dir), feeding stdin and
// failing the test on a non-zero exit.
func runMarchi(t *testing.T, binary, dataDir string, env []string, stdin string, args ...string) string {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Dir = dataDir
	cmd.Stdin = strings.NewReader(stdin)
	if env != nil {
		cmd.Env = append(os.Environ(), env...)
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("running marchi %v: %v\noutput:\n%s", args, err, out.String())
	}
	return out.String()
}

// killMidSync starts `sync` as a background process, lets it run briefly,
// then SIGKILLs it — no graceful shutdown, no cleanup, simulating a real
// crash (power loss, OOM kill, ...) rather than step 16's SIGINT/SIGTERM.
func killMidSync(t *testing.T, binary, dataDir, masterKey, email string) {
	t.Helper()
	cmd := exec.Command(binary, "sync", email)
	cmd.Dir = dataDir
	cmd.Env = append(os.Environ(), "MARCHI_MASTER_KEY="+masterKey)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	if err := cmd.Start(); err != nil {
		t.Fatalf("starting sync: %v", err)
	}

	// 150ms was the original margin (Phase 1 step 17); Phase 3's added
	// migrations/repos push sync's own startup-to-first-archive time high
	// enough that 150ms started landing before any message was archived
	// at all. 400ms restores a comfortable window without risking the
	// opposite failure (sync finishing before SIGKILL lands).
	time.Sleep(400 * time.Millisecond)

	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("SIGKILLing sync process: %v", err)
	}
	_ = cmd.Wait() // expected to report the process was killed; nothing to assert on the error itself
	t.Logf("killed sync mid-flight, partial output:\n%s", out.String())
}

func openTestDB(t *testing.T, dbPath string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("opening %s: %v", dbPath, err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func countEmails(t *testing.T, dbPath string) int {
	t.Helper()
	db := openTestDB(t, dbPath)
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM emails`).Scan(&n); err != nil {
		t.Fatalf("counting emails: %v", err)
	}
	return n
}

func assertNoDuplicateUIDs(t *testing.T, dbPath string) {
	t.Helper()
	db := openTestDB(t, dbPath)
	rows, err := db.Query(`SELECT uid, COUNT(*) c FROM emails GROUP BY uid HAVING c > 1`)
	if err != nil {
		t.Fatalf("checking for duplicate UIDs: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var uid, count int
		_ = rows.Scan(&uid, &count)
		t.Errorf("UID %d appears %d times — duplicate archival", uid, count)
	}
}

func assertNoGapsInUIDs(t *testing.T, dbPath string, total int) {
	t.Helper()
	db := openTestDB(t, dbPath)
	rows, err := db.Query(`SELECT uid FROM emails ORDER BY uid`)
	if err != nil {
		t.Fatalf("listing UIDs: %v", err)
	}
	defer rows.Close()

	want := 1
	for rows.Next() {
		var uid int
		if err := rows.Scan(&uid); err != nil {
			t.Fatal(err)
		}
		if uid != want {
			t.Errorf("UID sequence gap: expected %d, got %d", want, uid)
		}
		want++
	}
	if want-1 != total {
		t.Errorf("saw %d UIDs, want %d", want-1, total)
	}
}

// assertLocalPathsExist checks emails.local_path against disk. local_path
// is stored relative to the marchi subprocess's own working directory
// (dataDir, per runMarchi/killMidSync setting cmd.Dir) — this test
// process's cwd is the Go package directory instead, so paths are resolved
// against dataDir explicitly rather than passed to os.Stat as-is.
func assertLocalPathsExist(t *testing.T, dbPath, dataDir string) {
	t.Helper()
	db := openTestDB(t, dbPath)
	rows, err := db.Query(`SELECT id, local_path FROM emails`)
	if err != nil {
		t.Fatalf("listing local_paths: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id int
		var path string
		if err := rows.Scan(&id, &path); err != nil {
			t.Fatal(err)
		}
		fullPath := filepath.Join(dataDir, path)
		if _, err := os.Stat(fullPath); err != nil {
			t.Errorf("email %d: local_path %s does not exist: %v", id, fullPath, err)
		}
	}
}

func assertTmpDirEmpty(t *testing.T, dataDir string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dataDir, "data", "maildir", "accounts", "*", "mail", "*", "tmp", "*"))
	if err != nil {
		t.Fatalf("globbing tmp dirs: %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("expected no leftover files in tmp/ after the resumed sync's startup sweep, found: %v", matches)
	}
}
