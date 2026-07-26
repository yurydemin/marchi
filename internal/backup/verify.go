package backup

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// VerifyResult reports whether a backup in dir still matches its own
// manifest and whether the backed-up database passes SQLite's own
// consistency check.
type VerifyResult struct {
	OK                bool
	MismatchedFiles   []string // manifest entries whose SHA-256 no longer matches, or that are missing
	IntegrityCheckMsg string   // SQLite's own verdict — "ok" means healthy
}

// Verify recomputes SHA-256 for every file manifest.json in dir lists —
// catching truncation, bit rot, or a partial copy to another disk — then
// opens the backed-up database read-only and runs PRAGMA integrity_check,
// a deeper correctness check than a checksum alone gives: a file can be
// byte-for-byte what Run wrote and still have been an inconsistent
// snapshot if the Online Backup API itself had somehow failed silently.
func Verify(dir string) (VerifyResult, error) {
	manifestBytes, err := os.ReadFile(filepath.Join(dir, manifestFilename))
	if err != nil {
		return VerifyResult{}, fmt.Errorf("verify: reading manifest: %w", err)
	}
	var manifest Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return VerifyResult{}, fmt.Errorf("verify: parsing manifest: %w", err)
	}

	var mismatched []string
	for _, mf := range manifest.Files {
		sum, err := sha256File(filepath.Join(dir, mf.Name))
		if err != nil || sum != mf.SHA256 {
			mismatched = append(mismatched, mf.Name)
		}
	}

	integrityMsg, err := checkDatabaseIntegrity(filepath.Join(dir, dbFilename))
	if err != nil {
		return VerifyResult{}, fmt.Errorf("verify: checking database integrity: %w", err)
	}

	return VerifyResult{
		OK:                len(mismatched) == 0 && integrityMsg == "ok",
		MismatchedFiles:   mismatched,
		IntegrityCheckMsg: integrityMsg,
	}, nil
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// checkDatabaseIntegrity opens dbPath read-only (no migrations run
// against a backup copy — Verify must never mutate what it's checking)
// and runs SQLite's own PRAGMA integrity_check.
func checkDatabaseIntegrity(dbPath string) (string, error) {
	dsn := fmt.Sprintf("file:%s?mode=ro", dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return "", fmt.Errorf("opening %s: %w", dbPath, err)
	}
	defer db.Close()

	var msg string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&msg); err != nil {
		return "", fmt.Errorf("running integrity_check: %w", err)
	}
	return msg, nil
}
