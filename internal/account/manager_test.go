package account

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"path/filepath"
	"testing"

	"github.com/yurydemin/marchi/internal/db"
	"github.com/yurydemin/marchi/internal/db/repo"
	"github.com/yurydemin/marchi/internal/db/writer"
	"github.com/yurydemin/marchi/internal/domain"
	"github.com/yurydemin/marchi/internal/security/crypto"
)

func testMasterKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, crypto.KeySize)
	if _, err := io.ReadFull(rand.Reader, k); err != nil {
		t.Fatal(err)
	}
	return k
}

func openTestManager(t *testing.T, masterKey []byte) *Manager {
	t.Helper()
	path := filepath.Join(t.TempDir(), "marchi.db")
	sqlDB, err := db.Open(path)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })

	w := writer.New(sqlDB)
	t.Cleanup(func() { w.Close() })

	mgr, err := NewManager(repo.NewAccountsRepo(sqlDB, w), masterKey)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return mgr
}

func TestAddAccount_PasswordIsEncryptedAtRest(t *testing.T) {
	masterKey := testMasterKey(t)
	mgr := openTestManager(t, masterKey)

	a, err := mgr.AddAccount(context.Background(), AddAccountParams{
		Email:        "user@example.com",
		IMAPHost:     "imap.example.com",
		IMAPTLS:      domain.IMAPTLSSSL,
		IMAPPassword: "s3kr3t-imap-password",
	})
	if err != nil {
		t.Fatalf("AddAccount: %v", err)
	}

	if bytes.Contains(a.IMAPPasswordEncrypted, []byte("s3kr3t-imap-password")) {
		t.Fatal("stored blob contains the plaintext password")
	}

	// Round trip: the same subkey a real Manager derives must decrypt it
	// back to the original password.
	subkey, err := crypto.DeriveSubkey(masterKey, credentialSubkeyInfo)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := crypto.Decrypt(subkey, a.IMAPPasswordEncrypted, []byte(a.Email))
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if string(plain) != "s3kr3t-imap-password" {
		t.Errorf("decrypted password = %q", plain)
	}
}

func TestAddAccount_WrongEmailAADFailsDecryption(t *testing.T) {
	masterKey := testMasterKey(t)
	mgr := openTestManager(t, masterKey)

	a, err := mgr.AddAccount(context.Background(), AddAccountParams{
		Email:        "user@example.com",
		IMAPHost:     "imap.example.com",
		IMAPPassword: "s3kr3t",
	})
	if err != nil {
		t.Fatalf("AddAccount: %v", err)
	}

	subkey, err := crypto.DeriveSubkey(masterKey, credentialSubkeyInfo)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate the blob having been swapped into a different account's row.
	_, err = crypto.Decrypt(subkey, a.IMAPPasswordEncrypted, []byte("someone-else@example.com"))
	if err == nil {
		t.Error("expected decryption to fail with mismatched AAD (email)")
	}
}

func TestAddAccount_DefaultsUsernameAndPort(t *testing.T) {
	mgr := openTestManager(t, testMasterKey(t))

	a, err := mgr.AddAccount(context.Background(), AddAccountParams{
		Email:        "user@example.com",
		IMAPHost:     "imap.example.com",
		IMAPTLS:      domain.IMAPTLSSSL,
		IMAPPassword: "s3kr3t",
	})
	if err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	if a.IMAPUsername != "user@example.com" {
		t.Errorf("IMAPUsername = %q, want default to Email", a.IMAPUsername)
	}
	if a.IMAPPort != 993 {
		t.Errorf("IMAPPort = %d, want default 993 for ssl", a.IMAPPort)
	}
}

func TestAddAccount_DefaultPortForStartTLS(t *testing.T) {
	mgr := openTestManager(t, testMasterKey(t))
	a, err := mgr.AddAccount(context.Background(), AddAccountParams{
		Email:        "user@example.com",
		IMAPHost:     "imap.example.com",
		IMAPTLS:      domain.IMAPTLSStartTLS,
		IMAPPassword: "s3kr3t",
	})
	if err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	if a.IMAPPort != 143 {
		t.Errorf("IMAPPort = %d, want default 143 for starttls", a.IMAPPort)
	}
}

func TestAddAccount_ValidationErrors(t *testing.T) {
	mgr := openTestManager(t, testMasterKey(t))
	ctx := context.Background()

	cases := []struct {
		name string
		p    AddAccountParams
	}{
		{"missing email", AddAccountParams{IMAPHost: "h", IMAPPassword: "p"}},
		{"missing host", AddAccountParams{Email: "a@b.com", IMAPPassword: "p"}},
		{"missing password", AddAccountParams{Email: "a@b.com", IMAPHost: "h"}},
		{"bad port", AddAccountParams{Email: "a@b.com", IMAPHost: "h", IMAPPassword: "p", IMAPPort: 70000}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := mgr.AddAccount(ctx, tc.p); err == nil {
				t.Error("expected a validation error, got nil")
			}
		})
	}
}

func TestListAccounts(t *testing.T) {
	mgr := openTestManager(t, testMasterKey(t))
	ctx := context.Background()

	for _, email := range []string{"a@example.com", "b@example.com"} {
		if _, err := mgr.AddAccount(ctx, AddAccountParams{Email: email, IMAPHost: "h", IMAPPassword: "p"}); err != nil {
			t.Fatalf("AddAccount(%s): %v", email, err)
		}
	}

	accounts, err := mgr.ListAccounts(ctx)
	if err != nil {
		t.Fatalf("ListAccounts: %v", err)
	}
	if len(accounts) != 2 {
		t.Fatalf("got %d accounts, want 2", len(accounts))
	}
}

func TestAddAccount_DuplicateEmail(t *testing.T) {
	mgr := openTestManager(t, testMasterKey(t))
	ctx := context.Background()

	p := AddAccountParams{Email: "dup@example.com", IMAPHost: "h", IMAPPassword: "p"}
	if _, err := mgr.AddAccount(ctx, p); err != nil {
		t.Fatalf("first AddAccount: %v", err)
	}
	_, err := mgr.AddAccount(ctx, p)
	if !errors.Is(err, repo.ErrDuplicateEmail) {
		t.Errorf("second AddAccount error = %v, want ErrDuplicateEmail", err)
	}
}

func TestUpdateAccount_EmptyPasswordKeepsExisting(t *testing.T) {
	masterKey := testMasterKey(t)
	mgr := openTestManager(t, masterKey)
	ctx := context.Background()

	a, err := mgr.AddAccount(ctx, AddAccountParams{
		Email: "user@example.com", IMAPHost: "old.example.com", IMAPTLS: domain.IMAPTLSSSL,
		IMAPPassword: "original-password",
	})
	if err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	originalCiphertext := a.IMAPPasswordEncrypted

	active := true
	updated, err := mgr.UpdateAccount(ctx, a.ID, UpdateAccountParams{
		DisplayName: "Renamed", IMAPHost: "new.example.com", IMAPTLS: domain.IMAPTLSSSL,
		IMAPPassword: "", // deliberately omitted
		IsActive:     &active,
	})
	if err != nil {
		t.Fatalf("UpdateAccount: %v", err)
	}

	if updated.IMAPHost != "new.example.com" || updated.DisplayName != "Renamed" {
		t.Errorf("got %+v, want the new host/display name applied", updated)
	}
	if string(updated.IMAPPasswordEncrypted) != string(originalCiphertext) {
		t.Error("password ciphertext changed despite an empty IMAPPassword in the update")
	}

	plain, err := mgr.DecryptPassword(updated)
	if err != nil {
		t.Fatalf("DecryptPassword: %v", err)
	}
	if plain != "original-password" {
		t.Errorf("decrypted password = %q, want the original still intact", plain)
	}
}

func TestUpdateAccount_NewPasswordReplacesOldOne(t *testing.T) {
	mgr := openTestManager(t, testMasterKey(t))
	ctx := context.Background()

	a, err := mgr.AddAccount(ctx, AddAccountParams{
		Email: "user@example.com", IMAPHost: "h", IMAPTLS: domain.IMAPTLSSSL, IMAPPassword: "old-pw",
	})
	if err != nil {
		t.Fatalf("AddAccount: %v", err)
	}

	updated, err := mgr.UpdateAccount(ctx, a.ID, UpdateAccountParams{
		IMAPHost: "h", IMAPTLS: domain.IMAPTLSSSL, IMAPPassword: "new-pw",
	})
	if err != nil {
		t.Fatalf("UpdateAccount: %v", err)
	}

	plain, err := mgr.DecryptPassword(updated)
	if err != nil {
		t.Fatalf("DecryptPassword: %v", err)
	}
	if plain != "new-pw" {
		t.Errorf("decrypted password = %q, want new-pw", plain)
	}
}

func TestUpdateAccount_NilIsActiveKeepsExistingValue(t *testing.T) {
	mgr := openTestManager(t, testMasterKey(t))
	ctx := context.Background()

	a, err := mgr.AddAccount(ctx, AddAccountParams{
		Email: "user@example.com", IMAPHost: "h", IMAPTLS: domain.IMAPTLSSSL, IMAPPassword: "pw",
	})
	if err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	if !a.IsActive {
		t.Fatal("AddAccount should default IsActive to true")
	}

	updated, err := mgr.UpdateAccount(ctx, a.ID, UpdateAccountParams{
		IMAPHost: "h", IMAPTLS: domain.IMAPTLSSSL, IsActive: nil,
	})
	if err != nil {
		t.Fatalf("UpdateAccount: %v", err)
	}
	if !updated.IsActive {
		t.Error("IsActive flipped to false despite a nil IsActive in the update — omitting it must not silently pause sync")
	}
}

func TestUpdateAccount_UnknownID(t *testing.T) {
	mgr := openTestManager(t, testMasterKey(t))
	_, err := mgr.UpdateAccount(context.Background(), 999, UpdateAccountParams{IMAPHost: "h"})
	if err == nil {
		t.Error("expected an error updating a nonexistent account")
	}
}

// TestAddAccount_ReturnsPopulatedTimestamps guards against a regression:
// AddAccount used to just set a.ID after Create and return the same
// in-memory struct, whose CreatedAt/UpdatedAt were always the Go zero
// value since Create never reads back the DB-generated defaults — not
// noticed until the REST API started echoing this struct as JSON.
func TestAddAccount_ReturnsPopulatedTimestamps(t *testing.T) {
	mgr := openTestManager(t, testMasterKey(t))
	a, err := mgr.AddAccount(context.Background(), AddAccountParams{
		Email: "user@example.com", IMAPHost: "h", IMAPPassword: "p",
	})
	if err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	if a.CreatedAt.IsZero() {
		t.Error("CreatedAt is the zero value, want the DB-generated timestamp")
	}
	if a.UpdatedAt.IsZero() {
		t.Error("UpdatedAt is the zero value, want the DB-generated timestamp")
	}
}

func TestUpdateAccount_ReturnsRefreshedUpdatedAt(t *testing.T) {
	mgr := openTestManager(t, testMasterKey(t))
	ctx := context.Background()
	a, err := mgr.AddAccount(ctx, AddAccountParams{Email: "user@example.com", IMAPHost: "h", IMAPPassword: "p"})
	if err != nil {
		t.Fatalf("AddAccount: %v", err)
	}

	updated, err := mgr.UpdateAccount(ctx, a.ID, UpdateAccountParams{IMAPHost: "new-host"})
	if err != nil {
		t.Fatalf("UpdateAccount: %v", err)
	}
	if updated.UpdatedAt.IsZero() {
		t.Error("UpdatedAt is the zero value after an update, want the refreshed DB timestamp")
	}
	if updated.UpdatedAt.Before(a.CreatedAt) {
		t.Errorf("UpdatedAt (%v) is before the original CreatedAt (%v)", updated.UpdatedAt, a.CreatedAt)
	}
}
