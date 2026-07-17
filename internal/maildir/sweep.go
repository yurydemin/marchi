package maildir

import (
	"fmt"
	"os"
	"path/filepath"
)

// Sweep removes every file sitting directly in root's tmp/ directory —
// orphans left behind by a crash between Writer.Stage and Writer.Commit.
//
// Unlike a general-purpose MDA that might share tmp/ with other concurrent
// processes (and so waits out a grace period before sweeping, in case a
// file is still being written), MailVault is the sole writer into its own
// tmp/ dirs. A previous process is not still running by the time this
// runs, so any leftover file was never committed — its SQLite write either
// never happened or never succeeded, so the UID was never marked archived,
// and the next sync will simply re-fetch and re-stage it under a new name.
func Sweep(root string) error {
	tmp := filepath.Join(root, "tmp")
	entries, err := os.ReadDir(tmp)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("maildir: reading %s: %w", tmp, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if err := os.Remove(filepath.Join(tmp, e.Name())); err != nil {
			return fmt.Errorf("maildir: removing %s: %w", filepath.Join(tmp, e.Name()), err)
		}
	}
	return nil
}

// SweepAll runs Sweep across every account/folder under maildirRoot
// (config.yaml's storage.maildir_path; see FolderDir), the full startup
// cleanup. It walks the filesystem directly rather than going through the
// folders repo, so it still cleans up orphans left behind by a folder
// that's since been removed from tracking.
func SweepAll(maildirRoot string) error {
	pattern := filepath.Join(maildirRoot, "accounts", "*", "mail", "*")
	folderDirs, err := filepath.Glob(pattern)
	if err != nil {
		return fmt.Errorf("maildir: globbing folder dirs: %w", err)
	}
	for _, dir := range folderDirs {
		if err := Sweep(dir); err != nil {
			return err
		}
	}
	return nil
}
