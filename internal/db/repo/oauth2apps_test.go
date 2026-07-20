package repo

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/yurydemin/marchi/internal/db"
	"github.com/yurydemin/marchi/internal/db/writer"
	"github.com/yurydemin/marchi/internal/domain"
)

func openTestOAuth2AppsRepo(t *testing.T) *OAuth2AppsRepo {
	t.Helper()
	path := filepath.Join(t.TempDir(), "marchi.db")
	sqlDB, err := db.Open(path)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })

	w := writer.New(sqlDB)
	t.Cleanup(func() { w.Close() })

	return NewOAuth2AppsRepo(sqlDB, w)
}

func TestOAuth2AppsRepo_Get_NeverConfigured(t *testing.T) {
	repo := openTestOAuth2AppsRepo(t)
	if _, err := repo.Get(context.Background(), domain.OAuth2ProviderGoogle); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("Get before Upsert = %v, want sql.ErrNoRows", err)
	}
}

func TestOAuth2AppsRepo_UpsertAndGet_RoundTrips(t *testing.T) {
	repo := openTestOAuth2AppsRepo(t)
	ctx := context.Background()

	app := &domain.OAuth2App{
		Provider: domain.OAuth2ProviderGoogle, ClientID: "client-123.apps.googleusercontent.com",
		ClientSecretEncrypted: []byte("encrypted-secret"), RedirectURL: "https://marchi.local/oauth2/google/callback",
	}
	if err := repo.Upsert(ctx, app); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, err := repo.Get(ctx, domain.OAuth2ProviderGoogle)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ClientID != app.ClientID || got.RedirectURL != app.RedirectURL {
		t.Errorf("got = %+v, want matching ClientID/RedirectURL from %+v", got, app)
	}
	if string(got.ClientSecretEncrypted) != "encrypted-secret" {
		t.Errorf("ClientSecretEncrypted = %q, want %q", got.ClientSecretEncrypted, "encrypted-secret")
	}
	if got.UpdatedAt.IsZero() {
		t.Error("UpdatedAt is zero, want a timestamp")
	}
}

func TestOAuth2AppsRepo_BothProvidersIndependent(t *testing.T) {
	repo := openTestOAuth2AppsRepo(t)
	ctx := context.Background()

	if err := repo.Upsert(ctx, &domain.OAuth2App{
		Provider: domain.OAuth2ProviderGoogle, ClientID: "google-client",
		ClientSecretEncrypted: []byte("google-secret"), RedirectURL: "https://x/google/callback",
	}); err != nil {
		t.Fatalf("Upsert google: %v", err)
	}
	if err := repo.Upsert(ctx, &domain.OAuth2App{
		Provider: domain.OAuth2ProviderMicrosoft, ClientID: "ms-client",
		ClientSecretEncrypted: []byte("ms-secret"), RedirectURL: "https://x/microsoft/callback",
	}); err != nil {
		t.Fatalf("Upsert microsoft: %v", err)
	}

	apps, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(apps) != 2 {
		t.Fatalf("got %d apps, want 2 (google and microsoft coexist)", len(apps))
	}

	google, err := repo.Get(ctx, domain.OAuth2ProviderGoogle)
	if err != nil || google.ClientID != "google-client" {
		t.Errorf("Get(google) = %+v, %v", google, err)
	}
	microsoft, err := repo.Get(ctx, domain.OAuth2ProviderMicrosoft)
	if err != nil || microsoft.ClientID != "ms-client" {
		t.Errorf("Get(microsoft) = %+v, %v", microsoft, err)
	}
}

func TestOAuth2AppsRepo_Upsert_OverwritesExistingProviderRow(t *testing.T) {
	repo := openTestOAuth2AppsRepo(t)
	ctx := context.Background()

	if err := repo.Upsert(ctx, &domain.OAuth2App{
		Provider: domain.OAuth2ProviderGoogle, ClientID: "first-client",
		ClientSecretEncrypted: []byte("first-secret"), RedirectURL: "https://x/callback",
	}); err != nil {
		t.Fatalf("first Upsert: %v", err)
	}
	if err := repo.Upsert(ctx, &domain.OAuth2App{
		Provider: domain.OAuth2ProviderGoogle, ClientID: "second-client",
		ClientSecretEncrypted: []byte("second-secret"), RedirectURL: "https://x/callback",
	}); err != nil {
		t.Fatalf("second Upsert: %v", err)
	}

	got, err := repo.Get(ctx, domain.OAuth2ProviderGoogle)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ClientID != "second-client" {
		t.Errorf("ClientID = %q, want %q (second Upsert's value, no duplicate row)", got.ClientID, "second-client")
	}
}
