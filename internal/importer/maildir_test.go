package importer

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWalkMaildir_ReadsCurBeforeNew(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "new", "2.eml"), "Subject: new-two\r\n\r\nbody\r\n")
	writeFile(t, filepath.Join(root, "cur", "1.eml:2,S"), "Subject: cur-one\r\n\r\nbody\r\n")
	writeFile(t, filepath.Join(root, "tmp", "0.eml"), "Subject: should-be-ignored\r\n\r\nbody\r\n")

	var got []string
	if err := WalkMaildir(root, func(raw []byte) error {
		got = append(got, string(raw))
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if len(got) != 2 {
		t.Fatalf("got %d messages, want 2 (tmp/ must be skipped): %q", len(got), got)
	}
	if got[0] != "Subject: cur-one\r\n\r\nbody\r\n" {
		t.Errorf("first message = %q, want the cur/ one first", got[0])
	}
	if got[1] != "Subject: new-two\r\n\r\nbody\r\n" {
		t.Errorf("second message = %q, want the new/ one second", got[1])
	}
}

func TestWalkMaildir_MissingSubdirsAreNotAnError(t *testing.T) {
	root := t.TempDir() // no cur/ or new/ at all
	calls := 0
	if err := WalkMaildir(root, func(raw []byte) error {
		calls++
		return nil
	}); err != nil {
		t.Fatalf("WalkMaildir on an empty dir should not error: %v", err)
	}
	if calls != 0 {
		t.Errorf("calls = %d, want 0", calls)
	}
}
