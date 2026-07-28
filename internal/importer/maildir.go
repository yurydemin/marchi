package importer

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// WalkMaildir reads path as a single Maildir folder — a directory with
// cur/ and new/ subdirectories, each file already holding one complete
// raw RFC 5322 message, exactly the format internal/maildir.Writer itself
// produces — and calls fn once per file, cur/ before new/ and
// lexicographically within each, stopping at the first error either
// reading a file or fn returns.
//
// tmp/ is deliberately never read: by Maildir convention it holds
// in-progress deliveries that haven't been atomically linked into new/
// yet, so anything sitting there either belongs to some other, unrelated
// process or is a leftover from an interrupted write — never a complete
// message safe to import.
func WalkMaildir(path string, fn func(raw []byte) error) error {
	for _, sub := range []string{"cur", "new"} {
		if err := walkMaildirSub(filepath.Join(path, sub), fn); err != nil {
			return err
		}
	}
	return nil
}

func walkMaildirSub(dir string, fn func(raw []byte) error) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // a Maildir with no new/ (or no cur/) yet is still valid, just empty on that side
		}
		return fmt.Errorf("importer: reading maildir directory %q: %w", dir, err)
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.Type().IsRegular() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return fmt.Errorf("importer: reading maildir message %q: %w", filepath.Join(dir, name), err)
		}
		if err := fn(raw); err != nil {
			return err
		}
	}
	return nil
}
