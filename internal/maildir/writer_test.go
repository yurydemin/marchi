package maildir

import (
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"testing"
)

func newTestWriter(t *testing.T) (*Writer, Layout) {
	t.Helper()
	l, err := NewLayout(t.TempDir())
	if err != nil {
		t.Fatalf("NewLayout: %v", err)
	}
	return NewWriter(l, "test-host"), l
}

// Maildir filenames: {unix_time}.{pid}_{counter}.{host}:2,{flags}
var filenamePattern = regexp.MustCompile(`^\d+\.\d+_\d+\.[^:]+:2,[A-Za-z0-9]*$`)

func TestStage_WritesIntoTmpWithSpecFilename(t *testing.T) {
	w, l := newTestWriter(t)

	tmpPath, err := w.Stage([]byte("From: a@b.com\r\n\r\nhello"), "S")
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}

	if filepath.Dir(tmpPath) != l.Tmp {
		t.Errorf("staged into %s, want %s", filepath.Dir(tmpPath), l.Tmp)
	}
	name := filepath.Base(tmpPath)
	if !filenamePattern.MatchString(name) {
		t.Errorf("filename %q doesn't match Maildir spec pattern", name)
	}

	data, err := os.ReadFile(tmpPath)
	if err != nil {
		t.Fatalf("reading staged file: %v", err)
	}
	if string(data) != "From: a@b.com\r\n\r\nhello" {
		t.Errorf("staged content = %q", data)
	}
}

func TestCommit_MovesFromTmpToNewSameName(t *testing.T) {
	w, l := newTestWriter(t)

	tmpPath, err := w.Stage([]byte("content"), "")
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	name := filepath.Base(tmpPath)

	finalPath, err := w.Commit(tmpPath)
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}

	if filepath.Dir(finalPath) != l.New {
		t.Errorf("committed into %s, want %s", filepath.Dir(finalPath), l.New)
	}
	if filepath.Base(finalPath) != name {
		t.Errorf("filename changed on commit: %s -> %s", name, filepath.Base(finalPath))
	}
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Errorf("expected tmp file gone after commit, stat err = %v", err)
	}
	data, err := os.ReadFile(finalPath)
	if err != nil {
		t.Fatalf("reading committed file: %v", err)
	}
	if string(data) != "content" {
		t.Errorf("committed content = %q", data)
	}
}

func TestDiscard_RemovesStagedFile(t *testing.T) {
	w, _ := newTestWriter(t)

	tmpPath, err := w.Stage([]byte("never committed"), "")
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if err := w.Discard(tmpPath); err != nil {
		t.Fatalf("Discard: %v", err)
	}
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Errorf("expected file gone after Discard, stat err = %v", err)
	}
}

func TestDiscard_AlreadyGoneIsNotAnError(t *testing.T) {
	w, l := newTestWriter(t)
	if err := w.Discard(filepath.Join(l.Tmp, "nonexistent")); err != nil {
		t.Errorf("Discard of a missing file should be a no-op, got: %v", err)
	}
}

func TestStage_UniqueNamesUnderConcurrency(t *testing.T) {
	w, l := newTestWriter(t)

	const n = 100
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := w.Stage([]byte("x"), ""); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("Stage: %v", err)
	}

	entries, err := os.ReadDir(l.Tmp)
	if err != nil {
		t.Fatalf("reading tmp dir: %v", err)
	}
	if len(entries) != n {
		t.Errorf("got %d files in tmp/, want %d (name collision under concurrency)", len(entries), n)
	}
}

func TestFlags_SortedInFilename(t *testing.T) {
	w, _ := newTestWriter(t)

	tmpPath, err := w.Stage([]byte("x"), "SFR") // deliberately out of order
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	name := filepath.Base(tmpPath)
	if !regexp.MustCompile(`:2,FRS$`).MatchString(name) {
		t.Errorf("filename %q flags not sorted to FRS", name)
	}
}

func TestFlags_EmptyProducesTrailingComma(t *testing.T) {
	w, _ := newTestWriter(t)
	tmpPath, err := w.Stage([]byte("x"), "")
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	name := filepath.Base(tmpPath)
	if !regexp.MustCompile(`:2,$`).MatchString(name) {
		t.Errorf("filename %q should end in bare ':2,' for no flags", name)
	}
}

func TestNewWriter_SanitizesHost(t *testing.T) {
	l, err := NewLayout(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// A host string with path-unsafe characters must not produce a filename
	// that escapes tmp/ or otherwise breaks Stage.
	w := NewWriter(l, "host:with/bad\\chars")
	tmpPath, err := w.Stage([]byte("x"), "")
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if filepath.Dir(tmpPath) != l.Tmp {
		t.Errorf("Stage with unsanitized host escaped tmp/: %s", tmpPath)
	}
	if !filenamePattern.MatchString(filepath.Base(tmpPath)) {
		t.Errorf("filename %q doesn't match Maildir spec pattern after host sanitization", filepath.Base(tmpPath))
	}
}
