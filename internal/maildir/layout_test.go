package maildir

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFolderDir(t *testing.T) {
	got := FolderDir("/data", 7, "INBOX")
	want := filepath.Join("/data", "accounts", "7", "mail", "INBOX")
	if got != want {
		t.Errorf("FolderDir = %q, want %q", got, want)
	}
}

func TestNewLayout_CreatesAllThreeDirs(t *testing.T) {
	root := t.TempDir()
	l, err := NewLayout(root)
	if err != nil {
		t.Fatalf("NewLayout: %v", err)
	}
	for name, dir := range map[string]string{"cur": l.Cur, "new": l.New, "tmp": l.Tmp} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("%s is not a directory", name)
		}
	}
}

func TestSafeFolderName(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain name unchanged", "INBOX", "INBOX"},
		{"IMAP hierarchy delimiter flattened, not nested", "[Gmail]/Sent Mail", "[Gmail]_Sent Mail"},
		{"windows-reserved colon", "Notes:Personal", "Notes_Personal"},
		{"backslash", `A\B`, "A_B"},
		{"trailing dot rejected by windows", "Archive.", "Archive"},
		{"trailing space rejected by windows", "Archive ", "Archive"},
		{"empty becomes placeholder", "", "_"},
		{"dot becomes placeholder", ".", "_"},
		{"dotdot becomes placeholder", "..", "_"},
		{"control character stripped", "A\x00B", "A_B"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SafeFolderName(tc.in); got != tc.want {
				t.Errorf("SafeFolderName(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestSafeFolderName_NeverContainsPathSeparator(t *testing.T) {
	// A folder name must never let its sanitized form span more than one
	// path segment — that would silently create nested directories.
	for _, in := range []string{"a/b/c", "a\\b\\c", "/etc/passwd", "..\\..\\windows"} {
		safe := SafeFolderName(in)
		if filepath.Base(safe) != safe {
			t.Errorf("SafeFolderName(%q) = %q spans multiple path segments", in, safe)
		}
	}
}
