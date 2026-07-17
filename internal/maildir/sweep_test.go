package maildir

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSweep_RemovesOrphanedTmpFiles(t *testing.T) {
	root := t.TempDir()
	l, err := NewLayout(root)
	if err != nil {
		t.Fatal(err)
	}

	orphan := filepath.Join(l.Tmp, "orphaned-file")
	if err := os.WriteFile(orphan, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	committed := filepath.Join(l.New, "committed-file")
	if err := os.WriteFile(committed, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := Sweep(root); err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Errorf("expected orphaned tmp file removed, stat err = %v", err)
	}
	if _, err := os.Stat(committed); err != nil {
		t.Errorf("Sweep must not touch new/: %v", err)
	}
}

func TestSweep_MissingTmpDirIsNotAnError(t *testing.T) {
	if err := Sweep(t.TempDir()); err != nil {
		t.Errorf("Sweep on a dir with no tmp/ should be a no-op, got: %v", err)
	}
}

func TestSweepAll_WalksEveryAccountAndFolder(t *testing.T) {
	dataDir := t.TempDir()

	dir1 := FolderDir(dataDir, 1, "INBOX")
	dir2 := FolderDir(dataDir, 1, "Archive")
	dir3 := FolderDir(dataDir, 2, "INBOX")

	var orphans []string
	for _, dir := range []string{dir1, dir2, dir3} {
		l, err := NewLayout(dir)
		if err != nil {
			t.Fatal(err)
		}
		orphan := filepath.Join(l.Tmp, "orphan")
		if err := os.WriteFile(orphan, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		orphans = append(orphans, orphan)
	}

	if err := SweepAll(dataDir); err != nil {
		t.Fatalf("SweepAll: %v", err)
	}

	for _, orphan := range orphans {
		if _, err := os.Stat(orphan); !os.IsNotExist(err) {
			t.Errorf("expected %s removed, stat err = %v", orphan, err)
		}
	}
}

func TestSweepAll_NoAccountsYetIsNotAnError(t *testing.T) {
	if err := SweepAll(t.TempDir()); err != nil {
		t.Errorf("SweepAll on a fresh data dir should be a no-op, got: %v", err)
	}
}
