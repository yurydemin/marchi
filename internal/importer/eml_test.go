package importer

import (
	"path/filepath"
	"testing"
)

func TestWalkEML_Directory(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "b.eml"), "Subject: b\r\n\r\nbody\r\n")
	writeFile(t, filepath.Join(dir, "a.EML"), "Subject: a\r\n\r\nbody\r\n") // case-insensitive extension match
	writeFile(t, filepath.Join(dir, "notes.txt"), "not an email")

	var got []string
	if err := WalkEML(dir, func(raw []byte) error {
		got = append(got, string(raw))
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if len(got) != 2 {
		t.Fatalf("got %d messages, want 2 (notes.txt must be excluded): %q", len(got), got)
	}
	if got[0] != "Subject: a\r\n\r\nbody\r\n" {
		t.Errorf("first message = %q, want a.EML first (lexicographic order)", got[0])
	}
	if got[1] != "Subject: b\r\n\r\nbody\r\n" {
		t.Errorf("second message = %q, want b.eml second", got[1])
	}
}

func TestWalkEML_SingleFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "one.eml")
	writeFile(t, path, "Subject: solo\r\n\r\nbody\r\n")

	var got []string
	if err := WalkEML(path, func(raw []byte) error {
		got = append(got, string(raw))
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if len(got) != 1 || got[0] != "Subject: solo\r\n\r\nbody\r\n" {
		t.Fatalf("got %q, want exactly the one file's content", got)
	}
}
