// Package maildir writes archived emails to disk in Maildir format
// (FR-ST-01): {data_dir}/accounts/{account_id}/mail/{folder_safe_name}/{cur,new,tmp}/,
// one file per message, filenames per the Maildir spec.
package maildir

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Layout is the {cur,new,tmp} directory triple for one account+folder pair.
type Layout struct {
	Cur string
	New string
	Tmp string
}

// FolderDir returns one account/folder's Maildir root under maildirRoot
// (config.yaml's storage.maildir_path — FR-ST-01's "{data_dir}" in
// "{data_dir}/accounts/{account_id}/mail/{folder_safe_name}/" means this
// configurable maildir storage root, not necessarily the top-level
// app.data_dir; database/search/cache each get their own root the same way).
func FolderDir(maildirRoot string, accountID int64, folderSafeName string) string {
	return filepath.Join(maildirRoot, "accounts", strconv.FormatInt(accountID, 10), "mail", folderSafeName)
}

// NewLayout builds the {cur,new,tmp} triple under root, creating all three
// if they don't already exist.
func NewLayout(root string) (Layout, error) {
	l := Layout{
		Cur: filepath.Join(root, "cur"),
		New: filepath.Join(root, "new"),
		Tmp: filepath.Join(root, "tmp"),
	}
	for _, d := range []string{l.Cur, l.New, l.Tmp} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return Layout{}, fmt.Errorf("maildir: creating %s: %w", d, err)
		}
	}
	return l, nil
}

// unsafeFolderChars covers Windows' reserved filename characters plus
// control characters and the path separators — sanitizing against the
// strictest target OS keeps the same folder-derived directory name valid
// on Linux/macOS/Windows alike (NFR-DP-01's cross-platform requirement).
var unsafeFolderChars = regexp.MustCompile(`[<>:"/\\|?*\x00-\x1f]`)

// SafeFolderName turns an IMAP folder name into a single safe path segment
// (FR-ST-01's {folder_safe_name}). '/' — the most common offender, since
// IMAP folder hierarchies use it as a delimiter — becomes '_' rather than
// being preserved as a real path separator: one IMAP folder is always one
// directory here, never a nested tree.
func SafeFolderName(name string) string {
	safe := unsafeFolderChars.ReplaceAllString(name, "_")
	safe = strings.TrimRight(safe, " .") // Windows disallows trailing space/dot
	if safe == "" || safe == "." || safe == ".." {
		return "_"
	}
	return safe
}
