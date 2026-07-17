package masterkey

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func testParams() Argon2Params {
	// Deliberately tiny so tests run fast; production defaults live in
	// config.Argon2Config (65536 KiB / 3 / 4).
	return Argon2Params{Memory: 8 * 1024, Iterations: 1, Parallelism: 1}
}

func TestUnlock_BootstrapsOnFirstRun(t *testing.T) {
	dir := t.TempDir()
	saltPath, verifyPath := SaltPath(dir), VerifyPath(dir)

	if !IsFirstRun(saltPath) {
		t.Fatal("expected IsFirstRun true before any salt file exists")
	}

	key, err := Unlock("correct horse battery staple", saltPath, verifyPath, testParams())
	if err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	if len(key) != 32 {
		t.Errorf("key length = %d, want 32", len(key))
	}
	if IsFirstRun(saltPath) {
		t.Error("expected IsFirstRun false after bootstrap")
	}
	if _, err := os.Stat(saltPath); err != nil {
		t.Errorf("salt file not created: %v", err)
	}
	if _, err := os.Stat(verifyPath); err != nil {
		t.Errorf("verifier file not created: %v", err)
	}
}

func TestUnlock_CorrectPasswordOnSecondRun(t *testing.T) {
	dir := t.TempDir()
	saltPath, verifyPath := SaltPath(dir), VerifyPath(dir)
	password := "correct horse battery staple"

	key1, err := Unlock(password, saltPath, verifyPath, testParams())
	if err != nil {
		t.Fatalf("bootstrap Unlock: %v", err)
	}

	key2, err := Unlock(password, saltPath, verifyPath, testParams())
	if err != nil {
		t.Fatalf("second Unlock: %v", err)
	}
	if string(key1) != string(key2) {
		t.Error("same password should re-derive the same key across runs")
	}
}

func TestUnlock_IncorrectPasswordRejected(t *testing.T) {
	dir := t.TempDir()
	saltPath, verifyPath := SaltPath(dir), VerifyPath(dir)

	if _, err := Unlock("correct horse battery staple", saltPath, verifyPath, testParams()); err != nil {
		t.Fatalf("bootstrap Unlock: %v", err)
	}

	_, err := Unlock("wrong horse battery staple!!", saltPath, verifyPath, testParams())
	if !errors.Is(err, ErrIncorrectPassword) {
		t.Errorf("got %v, want ErrIncorrectPassword", err)
	}
}

func TestUnlock_PasswordTooShort(t *testing.T) {
	dir := t.TempDir()
	saltPath, verifyPath := SaltPath(dir), VerifyPath(dir)

	_, err := Unlock("short", saltPath, verifyPath, testParams())
	if !errors.Is(err, ErrPasswordTooShort) {
		t.Errorf("got %v, want ErrPasswordTooShort", err)
	}
	if !IsFirstRun(saltPath) {
		t.Error("a rejected too-short password must not bootstrap a salt file")
	}
}

func TestUnlock_CorruptedStore_SaltWithoutVerifier(t *testing.T) {
	dir := t.TempDir()
	saltPath, verifyPath := SaltPath(dir), VerifyPath(dir)

	if _, err := Unlock("correct horse battery staple", saltPath, verifyPath, testParams()); err != nil {
		t.Fatalf("bootstrap Unlock: %v", err)
	}
	if err := os.Remove(verifyPath); err != nil {
		t.Fatal(err)
	}

	_, err := Unlock("correct horse battery staple", saltPath, verifyPath, testParams())
	if !errors.Is(err, ErrCorruptedStore) {
		t.Errorf("got %v, want ErrCorruptedStore", err)
	}
}

func TestUnlock_DifferentPasswordsYieldDifferentKeys(t *testing.T) {
	dir1, dir2 := t.TempDir(), t.TempDir()
	key1, err := Unlock("password number one!!", SaltPath(dir1), VerifyPath(dir1), testParams())
	if err != nil {
		t.Fatal(err)
	}
	key2, err := Unlock("password number two!!", SaltPath(dir2), VerifyPath(dir2), testParams())
	if err != nil {
		t.Fatal(err)
	}
	if string(key1) == string(key2) {
		t.Error("different passwords must not derive the same key")
	}
}

func TestUnlock_SaltFileIsNotWorldReadable(t *testing.T) {
	dir := t.TempDir()
	saltPath, verifyPath := SaltPath(dir), VerifyPath(dir)
	if _, err := Unlock("correct horse battery staple", saltPath, verifyPath, testParams()); err != nil {
		t.Fatal(err)
	}

	for _, p := range []string{saltPath, verifyPath} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s has overly permissive mode %v", p, info.Mode().Perm())
		}
	}
}

func TestSaltPath_VerifyPath(t *testing.T) {
	dir := "/tmp/example-data"
	if got, want := SaltPath(dir), filepath.Join(dir, ".salt"); got != want {
		t.Errorf("SaltPath = %q, want %q", got, want)
	}
	if got, want := VerifyPath(dir), filepath.Join(dir, ".mk-verify"); got != want {
		t.Errorf("VerifyPath = %q, want %q", got, want)
	}
}
