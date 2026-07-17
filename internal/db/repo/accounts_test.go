package repo

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/yurydemin/marchi/internal/db"
	"github.com/yurydemin/marchi/internal/db/writer"
	"github.com/yurydemin/marchi/internal/domain"
)

func openTestRepo(t *testing.T) (*AccountsRepo, writer.Writer) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mailvault.db")
	sqlDB, err := db.Open(path)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })

	w := writer.New(sqlDB)
	t.Cleanup(func() { w.Close() })

	return NewAccountsRepo(sqlDB, w), w
}

func TestAccountsRepo_CreateAndList(t *testing.T) {
	repo, _ := openTestRepo(t)
	ctx := context.Background()

	id, err := repo.Create(ctx, &domain.Account{
		Email:                 "user@example.com",
		DisplayName:           "Work",
		IMAPHost:              "imap.example.com",
		IMAPPort:              993,
		IMAPTLS:               domain.IMAPTLSSSL,
		IMAPUsername:          "user@example.com",
		IMAPPasswordEncrypted: []byte("ciphertext-not-plaintext"),
		IsActive:              true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if id == 0 {
		t.Fatal("expected a non-zero id")
	}

	accounts, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(accounts) != 1 {
		t.Fatalf("got %d accounts, want 1", len(accounts))
	}

	a := accounts[0]
	if a.ID != id {
		t.Errorf("ID = %d, want %d", a.ID, id)
	}
	if a.Email != "user@example.com" {
		t.Errorf("Email = %q", a.Email)
	}
	if a.DisplayName != "Work" {
		t.Errorf("DisplayName = %q", a.DisplayName)
	}
	if a.IMAPTLS != domain.IMAPTLSSSL {
		t.Errorf("IMAPTLS = %v, want ssl", a.IMAPTLS)
	}
	if string(a.IMAPPasswordEncrypted) != "ciphertext-not-plaintext" {
		t.Errorf("IMAPPasswordEncrypted = %q", a.IMAPPasswordEncrypted)
	}
	if !a.IsActive {
		t.Error("expected IsActive true")
	}
	if a.CreatedAt.IsZero() {
		t.Error("expected CreatedAt to be populated")
	}
	if a.OAuth2Provider != "" {
		t.Errorf("OAuth2Provider = %q, want empty (never set)", a.OAuth2Provider)
	}
	if a.OAuth2TokenEncrypted != nil {
		t.Errorf("OAuth2TokenEncrypted = %v, want nil (never set)", a.OAuth2TokenEncrypted)
	}
}

func TestAccountsRepo_DuplicateEmailRejected(t *testing.T) {
	repo, _ := openTestRepo(t)
	ctx := context.Background()

	acct := &domain.Account{Email: "dup@example.com", IMAPHost: "imap.example.com", IMAPPort: 993, IsActive: true}
	if _, err := repo.Create(ctx, acct); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	_, err := repo.Create(ctx, acct)
	if !errors.Is(err, ErrDuplicateEmail) {
		t.Errorf("second Create error = %v, want ErrDuplicateEmail", err)
	}
}

func TestAccountsRepo_GetByEmail(t *testing.T) {
	repo, _ := openTestRepo(t)
	ctx := context.Background()

	if _, err := repo.Create(ctx, &domain.Account{Email: "find-me@example.com", IMAPHost: "h", IMAPPort: 993, IsActive: true}); err != nil {
		t.Fatal(err)
	}

	a, err := repo.GetByEmail(ctx, "find-me@example.com")
	if err != nil {
		t.Fatalf("GetByEmail: %v", err)
	}
	if a.Email != "find-me@example.com" {
		t.Errorf("Email = %q", a.Email)
	}

	_, err = repo.GetByEmail(ctx, "nobody@example.com")
	if err == nil {
		t.Error("expected an error for a missing email")
	}
}

func TestAccountsRepo_ListEmpty(t *testing.T) {
	repo, _ := openTestRepo(t)
	accounts, err := repo.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(accounts) != 0 {
		t.Errorf("got %d accounts, want 0", len(accounts))
	}
}
