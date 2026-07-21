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
	if got, want := DEKPath(dir), filepath.Join(dir, ".dek"); got != want {
		t.Errorf("DEKPath = %q, want %q", got, want)
	}
}

func TestLoadOrCreateDEK_BootstrapsThenPersists(t *testing.T) {
	dir := t.TempDir()
	dekPath := DEKPath(dir)
	masterKey, err := Unlock("correct horse battery staple", SaltPath(dir), VerifyPath(dir), testParams())
	if err != nil {
		t.Fatalf("Unlock: %v", err)
	}

	dek1, err := LoadOrCreateDEK(masterKey, dekPath)
	if err != nil {
		t.Fatalf("LoadOrCreateDEK (bootstrap): %v", err)
	}
	if len(dek1) != 32 {
		t.Fatalf("DEK length = %d, want 32", len(dek1))
	}
	if _, err := os.Stat(dekPath); err != nil {
		t.Fatalf("DEK file not created: %v", err)
	}

	dek2, err := LoadOrCreateDEK(masterKey, dekPath)
	if err != nil {
		t.Fatalf("LoadOrCreateDEK (existing): %v", err)
	}
	if string(dek1) != string(dek2) {
		t.Error("re-loading the DEK must return the same bytes, not generate a new one")
	}
}

func TestLoadOrCreateDEK_WrongMasterKeyFails(t *testing.T) {
	dir := t.TempDir()
	dekPath := DEKPath(dir)
	masterKey, err := Unlock("correct horse battery staple", SaltPath(dir), VerifyPath(dir), testParams())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateDEK(masterKey, dekPath); err != nil {
		t.Fatalf("bootstrapping DEK: %v", err)
	}

	wrongKey := make([]byte, 32)
	if _, err := LoadOrCreateDEK(wrongKey, dekPath); err == nil {
		t.Error("expected an error unwrapping the DEK with the wrong master key, got nil")
	}
}

func TestUnlockDEK_SameDEKAcrossCalls(t *testing.T) {
	dir := t.TempDir()
	password := "correct horse battery staple"
	saltPath, verifyPath, dekPath := SaltPath(dir), VerifyPath(dir), DEKPath(dir)

	dek1, err := UnlockDEK(password, saltPath, verifyPath, dekPath, testParams())
	if err != nil {
		t.Fatalf("UnlockDEK (bootstrap): %v", err)
	}
	dek2, err := UnlockDEK(password, saltPath, verifyPath, dekPath, testParams())
	if err != nil {
		t.Fatalf("UnlockDEK (second call): %v", err)
	}
	if string(dek1) != string(dek2) {
		t.Error("UnlockDEK must return the same DEK across calls with the same password")
	}
}

// TestChangePassword_DEKSurvivesRotation is this feature's core promise:
// after rotating the password, the OLD password no longer unlocks the
// vault, the NEW password does, and — critically — it unlocks to the
// EXACT SAME DEK, meaning nothing encrypted under it (IMAP passwords,
// OAuth2 tokens, S3 credentials, already-uploaded S3 objects) needs any
// re-encryption at all.
func TestChangePassword_DEKSurvivesRotation(t *testing.T) {
	dir := t.TempDir()
	saltPath, verifyPath, dekPath := SaltPath(dir), VerifyPath(dir), DEKPath(dir)
	oldPassword, newPassword := "correct horse battery staple", "new correct horse battery staple"

	dekBefore, err := UnlockDEK(oldPassword, saltPath, verifyPath, dekPath, testParams())
	if err != nil {
		t.Fatalf("initial UnlockDEK: %v", err)
	}

	if err := ChangePassword(oldPassword, newPassword, dir, testParams()); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}

	if _, err := UnlockDEK(oldPassword, saltPath, verifyPath, dekPath, testParams()); !errors.Is(err, ErrIncorrectPassword) {
		t.Errorf("old password after rotation: got %v, want ErrIncorrectPassword", err)
	}

	dekAfter, err := UnlockDEK(newPassword, saltPath, verifyPath, dekPath, testParams())
	if err != nil {
		t.Fatalf("UnlockDEK with new password: %v", err)
	}
	if string(dekBefore) != string(dekAfter) {
		t.Error("DEK changed across password rotation — every encrypted secret would now be unreadable")
	}
}

func TestChangePassword_WrongOldPasswordRejected(t *testing.T) {
	dir := t.TempDir()
	if _, err := UnlockDEK("correct horse battery staple", SaltPath(dir), VerifyPath(dir), DEKPath(dir), testParams()); err != nil {
		t.Fatal(err)
	}

	err := ChangePassword("wrong horse battery staple!!", "new correct horse battery staple", dir, testParams())
	if !errors.Is(err, ErrIncorrectPassword) {
		t.Errorf("got %v, want ErrIncorrectPassword", err)
	}
}

func TestChangePassword_NewPasswordTooShort(t *testing.T) {
	dir := t.TempDir()
	oldPassword := "correct horse battery staple"
	if _, err := UnlockDEK(oldPassword, SaltPath(dir), VerifyPath(dir), DEKPath(dir), testParams()); err != nil {
		t.Fatal(err)
	}

	err := ChangePassword(oldPassword, "short", dir, testParams())
	if !errors.Is(err, ErrPasswordTooShort) {
		t.Errorf("got %v, want ErrPasswordTooShort", err)
	}

	// A rejected rotation must not have touched the store: the old
	// password should still work.
	if _, err := UnlockDEK(oldPassword, SaltPath(dir), VerifyPath(dir), DEKPath(dir), testParams()); err != nil {
		t.Errorf("old password broken after a rejected rotation attempt: %v", err)
	}
}
