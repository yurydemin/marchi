package maildir

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Writer stages messages into tmp/ and commits them into new/ only once the
// caller confirms the corresponding SQLite write succeeded.
//
// This is a deliberate correction to the tech spec's literal data-flow
// ordering: writing straight into new/ before the SQLite commit would leave
// an unreferenced, non-deterministically-named orphan file on a crash
// between the two (Maildir filenames embed a counter, so a retry doesn't
// overwrite the orphan — it just writes another file next to it). Staging
// in tmp/ — Maildir's own designated safe-to-sweep area — and renaming into
// new/ only after the SQLite transaction commits keeps a crash mid-write
// cleanly recoverable: Sweep/SweepAll clears tmp/ at startup, and since the
// UID was never marked archived, the next sync just re-fetches and
// re-stages under a fresh name.
type Writer struct {
	layout Layout
	host   string
	pid    int

	mu      sync.Mutex
	counter int64
}

// NewWriter builds a Writer over layout. host is sanitized the same way
// folder names are, since it goes into the filename too and hostnames can
// contain characters that aren't safe there (e.g. nothing stops a machine
// being named with a ':').
func NewWriter(layout Layout, host string) *Writer {
	return &Writer{
		layout: layout,
		host:   SafeFolderName(host),
		pid:    os.Getpid(),
	}
}

// Stage writes data into tmp/ under a freshly generated Maildir filename
// and returns its path. flags is the Maildir info-section flag string
// (e.g. "S", "RF", or "" for none) — already translated from IMAP flags by
// the caller; this package only knows the Maildir filename format, not
// IMAP semantics.
//
// The caller must follow up with Commit (once its SQLite write succeeds)
// or Discard (otherwise). A Stage left dangling — neither committed nor
// discarded, e.g. because the process crashed in between — is cleaned up
// by the next Sweep/SweepAll call.
func (w *Writer) Stage(data []byte, flags string) (tmpPath string, err error) {
	name := w.filename(flags)
	tmpPath = filepath.Join(w.layout.Tmp, name)

	f, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("maildir: creating %s: %w", tmpPath, err)
	}
	defer f.Close()

	if _, err := f.Write(data); err != nil {
		return "", fmt.Errorf("maildir: writing %s: %w", tmpPath, err)
	}
	if err := f.Sync(); err != nil {
		return "", fmt.Errorf("maildir: syncing %s: %w", tmpPath, err)
	}
	return tmpPath, nil
}

// FinalPath computes where tmpPath will land once committed, without
// touching the filesystem. Callers that need to record a message's path
// (e.g. emails.local_path) before the corresponding SQLite write commits —
// which per this package's own staging order happens before the Maildir
// Commit itself — use this instead of Commit's return value.
func (w *Writer) FinalPath(tmpPath string) string {
	return filepath.Join(w.layout.New, filepath.Base(tmpPath))
}

// Commit atomically moves a staged file from tmp/ into new/, keeping the
// same filename — that uniqueness is exactly why Stage generated it up
// front rather than leaving naming to Commit time.
func (w *Writer) Commit(tmpPath string) (finalPath string, err error) {
	finalPath = w.FinalPath(tmpPath)
	if err := os.Rename(tmpPath, finalPath); err != nil {
		return "", fmt.Errorf("maildir: committing %s: %w", tmpPath, err)
	}
	return finalPath, nil
}

// Discard removes a staged file that will never be committed (e.g. the
// corresponding SQLite write failed). Safe to call even if the file's
// already gone.
func (w *Writer) Discard(tmpPath string) error {
	if err := os.Remove(tmpPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("maildir: discarding %s: %w", tmpPath, err)
	}
	return nil
}

// filename builds {unix_time}.{pid}_{counter}.{host}:2,{flags} (FR-ST-01).
func (w *Writer) filename(flags string) string {
	w.mu.Lock()
	w.counter++
	c := w.counter
	w.mu.Unlock()

	return fmt.Sprintf("%d.%d_%d.%s:2,%s", time.Now().Unix(), w.pid, c, w.host, sortFlags(flags))
}

// sortFlags puts the Maildir info-section flags in ASCII order, as the spec
// requires ("2,FRS" is legal, "2,RFS" is not). Case is preserved: uppercase
// letters are the six standard flags (D,F,P,R,S,T), lowercase letters/digits
// are reserved for custom/experimental ones — this package doesn't judge
// which flags a caller passes, only orders them.
func sortFlags(flags string) string {
	if flags == "" {
		return ""
	}
	b := []byte(flags)
	sort.Slice(b, func(i, j int) bool { return b[i] < b[j] })
	return string(b)
}
