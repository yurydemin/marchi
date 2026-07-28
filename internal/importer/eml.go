package importer

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// WalkEML calls fn once per .eml file under path, in lexicographic
// filename order, stopping at the first error either reading a file or
// fn returns. path may be a single .eml file (fn is called once, for
// that file) or a directory, in which case every regular file directly
// inside it whose name ends in ".eml" (case-insensitive) is read — not
// recursive, since nothing else in this package walks nested folder
// structure out of a flat source either.
func WalkEML(path string, fn func(raw []byte) error) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("importer: reading %q: %w", path, err)
	}

	if !info.IsDir() {
		raw, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("importer: reading %q: %w", path, err)
		}
		return fn(raw)
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return fmt.Errorf("importer: reading directory %q: %w", path, err)
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.Type().IsRegular() && strings.EqualFold(filepath.Ext(e.Name()), ".eml") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		raw, err := os.ReadFile(filepath.Join(path, name))
		if err != nil {
			return fmt.Errorf("importer: reading %q: %w", filepath.Join(path, name), err)
		}
		if err := fn(raw); err != nil {
			return err
		}
	}
	return nil
}
