package oauth2config

import (
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"path/filepath"
	"testing"

	"github.com/yurydemin/marchi/internal/db"
	"github.com/yurydemin/marchi/internal/db/repo"
	"github.com/yurydemin/marchi/internal/db/writer"
	"github.com/yurydemin/marchi/internal/domain"
	"github.com/yurydemin/marchi/internal/security/crypto"
)

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "mailvault.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	w := writer.New(sqlDB)
	t.Cleanup(func() { w.Close() })

	masterKey := make([]byte, crypto.KeySize)
	if _, err := io.ReadFull(rand.Reader, masterKey); err != nil {
		t.Fatal(err)
	}

	mgr, err := NewManager(repo.NewOAuth2AppsRepo(sqlDB, w), masterKey)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return mgr
}

func TestManager_SaveAndGet_RoundTrips(t *testing.T) {
	mgr := newTestManager(t)
	ctx := context.Background()

	saved, err := mgr.Save(ctx, SaveParams{
		Provider: domain.OAuth2ProviderGoogle, ClientID: "client-123.apps.googleusercontent.com",
		ClientSecret: "GOCSPX-supersecret", RedirectURL: "https://mailvault.local/oauth2/google/callback",
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if saved.ClientID != "client-123.apps.googleusercontent.com" {
		t.Errorf("ClientID = %q", saved.ClientID)
	}
	if bytes.Contains(saved.ClientSecretEncrypted, []byte("GOCSPX-supersecret")) {
		t.Error("ClientSecretEncrypted contains the plaintext secret verbatim")
	}

	got, err := mgr.Get(ctx, domain.OAuth2ProviderGoogle)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	secret, err := mgr.DecryptClientSecret(got)
	if err != nil {
		t.Fatalf("DecryptClientSecret: %v", err)
	}
	if secret != "GOCSPX-supersecret" {
		t.Errorf("decrypted secret = %q, want GOCSPX-supersecret", secret)
	}
}

func TestManager_Save_EmptySecretOnUpdate_KeepsExisting(t *testing.T) {
	mgr := newTestManager(t)
	ctx := context.Background()

	if _, err := mgr.Save(ctx, SaveParams{
		Provider: domain.OAuth2ProviderGoogle, ClientID: "client-1", ClientSecret: "secret-1",
		RedirectURL: "https://x/callback",
	}); err != nil {
		t.Fatalf("first Save: %v", err)
	}

	updated, err := mgr.Save(ctx, SaveParams{
		Provider: domain.OAuth2ProviderGoogle, ClientID: "client-2", RedirectURL: "https://x/callback",
	})
	if err != nil {
		t.Fatalf("second Save: %v", err)
	}
	secret, err := mgr.DecryptClientSecret(updated)
	if err != nil {
		t.Fatalf("DecryptClientSecret: %v", err)
	}
	if secret != "secret-1" {
		t.Errorf("secret after keep-existing update = %q, want unchanged secret-1", secret)
	}
	if updated.ClientID != "client-2" {
		t.Errorf("ClientID = %q, want client-2 (this field was updated)", updated.ClientID)
	}
}

func TestManager_BuildApp_ReturnsDecryptedAndReadyApp(t *testing.T) {
	mgr := newTestManager(t)
	ctx := context.Background()

	if _, err := mgr.Save(ctx, SaveParams{
		Provider: domain.OAuth2ProviderMicrosoft, ClientID: "ms-client", ClientSecret: "ms-secret",
		RedirectURL: "https://x/microsoft/callback",
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	app, err := mgr.BuildApp(ctx, domain.OAuth2ProviderMicrosoft)
	if err != nil {
		t.Fatalf("BuildApp: %v", err)
	}
	if app.ClientID != "ms-client" || app.ClientSecret != "ms-secret" || app.Provider != domain.OAuth2ProviderMicrosoft {
		t.Errorf("app = %+v, want decrypted ms-client/ms-secret for microsoft", app)
	}
}

func TestManager_Save_UnknownProvider_Rejected(t *testing.T) {
	mgr := newTestManager(t)
	if _, err := mgr.Save(context.Background(), SaveParams{
		Provider: "yahoo", ClientID: "x", RedirectURL: "https://x/callback",
	}); err == nil {
		t.Error("Save with an unknown provider succeeded, want a validation error")
	}
}

func TestManager_Save_MissingClientID_Rejected(t *testing.T) {
	mgr := newTestManager(t)
	if _, err := mgr.Save(context.Background(), SaveParams{
		Provider: domain.OAuth2ProviderGoogle, RedirectURL: "https://x/callback",
	}); err == nil {
		t.Error("Save with no client_id succeeded, want a validation error")
	}
}

func TestManager_ProvidersDoNotDecryptEachOthersSecret(t *testing.T) {
	// Same client secret text saved for both providers must not be
	// interchangeable — each ciphertext is bound to its own provider via
	// AAD (oauth2AADFor), so swapping the encrypted blob between rows
	// (simulated here by decrypting google's ciphertext as if it were
	// microsoft's) must fail loudly instead of silently "working".
	mgr := newTestManager(t)
	ctx := context.Background()

	google, err := mgr.Save(ctx, SaveParams{
		Provider: domain.OAuth2ProviderGoogle, ClientID: "g", ClientSecret: "shared-secret-text",
		RedirectURL: "https://x/callback",
	})
	if err != nil {
		t.Fatalf("Save google: %v", err)
	}

	tampered := &domain.OAuth2App{Provider: domain.OAuth2ProviderMicrosoft, ClientSecretEncrypted: google.ClientSecretEncrypted}
	if _, err := mgr.DecryptClientSecret(tampered); err == nil {
		t.Error("decrypting google's ciphertext under microsoft's AAD succeeded, want an error")
	}
}
