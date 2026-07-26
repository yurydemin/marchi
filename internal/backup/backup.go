// Package backup produces a consistent, hot (no-downtime) snapshot of
// everything needed to restore a Marchi archive elsewhere: the SQLite
// database (via SQLite's Online Backup API, not a raw file copy — the
// database is live and under WAL, so copying the file directly could
// catch it mid-write), the Maildir tree, and the Master Key's wrapped key
// material (.salt/.mk-verify/.dek). A manifest of SHA-256 checksums lets
// Verify confirm the copy wasn't silently truncated or corrupted later.
//
// This intentionally never decrypts anything: .dek stays wrapped, and
// whatever's encrypted inside the database (IMAP passwords, OAuth2
// tokens, S3 credentials) stays encrypted in the backup too — restoring
// it elsewhere still requires the same Master Key password, exactly as
// it should.
package backup

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/yurydemin/marchi/internal/version"
)

const (
	dbFilename       = "marchi.db"
	maildirArchive   = "maildir.tar.gz"
	saltFilename     = ".salt"
	verifyFilename   = ".mk-verify"
	dekFilename      = ".dek"
	manifestFilename = "manifest.json"
)

// Manifest records what a backup contains — written into destDir itself
// so Verify has something to check the files against later, independent
// of whoever's running it or when.
type Manifest struct {
	MarchiVersion string         `json:"marchi_version"`
	CreatedAt     time.Time      `json:"created_at"`
	Files         []ManifestFile `json:"files"`
}

// ManifestFile is one backed-up file's name (relative to destDir), size,
// and SHA-256 — the file's SQLite database rules ordering is the fixed
// order Run writes it in.
type ManifestFile struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Run writes a complete backup into destDir. destDir must not already
// exist (or must be empty) — Run never overwrites an existing backup
// silently; the caller picks a fresh, ideally timestamped, destination
// per run (e.g. "/backups/marchi-2026-07-26T03-00-00Z").
//
// sqlDB is the same pool the caller's Single Writer uses. Backing it up
// via the Online Backup API rides on WAL's readers-don't-block-writer
// guarantee (internal/db.Open already enables WAL) — the archive keeps
// syncing normally for the whole duration of the backup.
func Run(ctx context.Context, sqlDB *sql.DB, dataDir, maildirRoot, destDir string) (Manifest, error) {
	if entries, err := os.ReadDir(destDir); err == nil && len(entries) > 0 {
		return Manifest{}, fmt.Errorf("backup: destination %q already exists and is not empty", destDir)
	} else if err != nil && !os.IsNotExist(err) {
		return Manifest{}, fmt.Errorf("backup: checking destination %q: %w", destDir, err)
	}
	if err := os.MkdirAll(destDir, 0o700); err != nil {
		return Manifest{}, fmt.Errorf("backup: creating destination %q: %w", destDir, err)
	}

	dbPath := filepath.Join(destDir, dbFilename)
	if err := backupDatabase(ctx, sqlDB, dbPath); err != nil {
		return Manifest{}, err
	}

	maildirPath := filepath.Join(destDir, maildirArchive)
	if err := tarGzipDir(maildirRoot, maildirPath); err != nil {
		return Manifest{}, fmt.Errorf("backup: archiving maildir: %w", err)
	}

	var files []ManifestFile
	for _, name := range []string{dbFilename, maildirArchive} {
		mf, err := manifestFileFor(destDir, name)
		if err != nil {
			return Manifest{}, err
		}
		files = append(files, mf)
	}

	// The Master Key material is optional per source file: .mk-verify
	// only exists once a password has actually been set (not on a vault
	// that's never been unlocked), and copying whichever of the three
	// exist is still a useful partial backup rather than a hard failure.
	for _, name := range []string{saltFilename, verifyFilename, dekFilename} {
		src := filepath.Join(dataDir, name)
		if _, err := os.Stat(src); os.IsNotExist(err) {
			continue
		}
		dst := filepath.Join(destDir, name)
		if err := copyFile(src, dst); err != nil {
			return Manifest{}, fmt.Errorf("backup: copying %s: %w", name, err)
		}
		mf, err := manifestFileFor(destDir, name)
		if err != nil {
			return Manifest{}, err
		}
		files = append(files, mf)
	}

	manifest := Manifest{
		MarchiVersion: version.Version,
		CreatedAt:     time.Now().UTC(),
		Files:         files,
	}
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return Manifest{}, fmt.Errorf("backup: encoding manifest: %w", err)
	}
	if err := os.WriteFile(filepath.Join(destDir, manifestFilename), manifestBytes, 0o600); err != nil {
		return Manifest{}, fmt.Errorf("backup: writing manifest: %w", err)
	}

	return manifest, nil
}

func manifestFileFor(destDir, name string) (ManifestFile, error) {
	path := filepath.Join(destDir, name)
	f, err := os.Open(path)
	if err != nil {
		return ManifestFile{}, fmt.Errorf("backup: hashing %s: %w", name, err)
	}
	defer f.Close()

	h := sha256.New()
	size, err := io.Copy(h, f)
	if err != nil {
		return ManifestFile{}, fmt.Errorf("backup: hashing %s: %w", name, err)
	}
	return ManifestFile{Name: name, SHA256: hex.EncodeToString(h.Sum(nil)), Size: size}, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}
