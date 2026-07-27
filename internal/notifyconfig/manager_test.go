package notifyconfig

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
	"github.com/yurydemin/marchi/internal/notify"
	"github.com/yurydemin/marchi/internal/security/crypto"
)

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "marchi.db"))
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

	mgr, err := NewManager(repo.NewNotificationSettingsRepo(sqlDB, w), masterKey)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return mgr
}

func TestManager_SaveAndGet_RoundTrips(t *testing.T) {
	mgr := newTestManager(t)
	ctx := context.Background()

	saved, err := mgr.Save(ctx, SaveParams{
		WebhookEnabled: true, WebhookURL: "https://example.com/hook", WebhookSecret: "hook-secret",
		EmailEnabled: true, SMTPHost: "smtp.example.com", SMTPPort: 587,
		SMTPUsername: "relay", SMTPPassword: "relay-pass", SMTPFrom: "marchi@example.com", SMTPTo: "admin@example.com",
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !saved.WebhookEnabled || saved.WebhookURL != "https://example.com/hook" {
		t.Errorf("saved webhook fields = %+v", saved)
	}
	if bytes.Contains(saved.WebhookSecretEncrypted, []byte("hook-secret")) {
		t.Error("WebhookSecretEncrypted contains the plaintext secret verbatim")
	}
	if bytes.Contains(saved.SMTPPasswordEncrypted, []byte("relay-pass")) {
		t.Error("SMTPPasswordEncrypted contains the plaintext password verbatim")
	}

	got, err := mgr.Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	webhookSecret, smtpPassword, err := mgr.DecryptSecrets(got)
	if err != nil {
		t.Fatalf("DecryptSecrets: %v", err)
	}
	if webhookSecret != "hook-secret" {
		t.Errorf("webhookSecret = %q, want %q", webhookSecret, "hook-secret")
	}
	if smtpPassword != "relay-pass" {
		t.Errorf("smtpPassword = %q, want %q", smtpPassword, "relay-pass")
	}
}

func TestManager_Save_EmptySecretsKeepExisting(t *testing.T) {
	mgr := newTestManager(t)
	ctx := context.Background()

	if _, err := mgr.Save(ctx, SaveParams{
		WebhookEnabled: true, WebhookURL: "https://example.com/hook", WebhookSecret: "original-secret",
	}); err != nil {
		t.Fatalf("first Save: %v", err)
	}

	// Resubmit with WebhookSecret left blank — the update-without-
	// re-pasting-the-secret convention every other Settings form uses.
	saved, err := mgr.Save(ctx, SaveParams{WebhookEnabled: true, WebhookURL: "https://example.com/hook-v2"})
	if err != nil {
		t.Fatalf("second Save: %v", err)
	}
	if saved.WebhookURL != "https://example.com/hook-v2" {
		t.Errorf("WebhookURL = %q, want the updated value", saved.WebhookURL)
	}

	webhookSecret, _, err := mgr.DecryptSecrets(saved)
	if err != nil {
		t.Fatalf("DecryptSecrets: %v", err)
	}
	if webhookSecret != "original-secret" {
		t.Errorf("webhookSecret = %q, want the original secret preserved", webhookSecret)
	}
}

func TestManager_Save_ValidatesRequiredFieldsPerChannel(t *testing.T) {
	mgr := newTestManager(t)
	ctx := context.Background()

	if _, err := mgr.Save(ctx, SaveParams{WebhookEnabled: true}); err == nil {
		t.Error("Save with WebhookEnabled but no URL = nil error, want an error")
	}
	if _, err := mgr.Save(ctx, SaveParams{EmailEnabled: true}); err == nil {
		t.Error("Save with EmailEnabled but no SMTP host = nil error, want an error")
	}
}

func TestManager_BuildNotifier_NeitherChannelEnabled_ReturnsNil(t *testing.T) {
	mgr := newTestManager(t)
	ctx := context.Background()

	saved, err := mgr.Save(ctx, SaveParams{})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	n, err := mgr.BuildNotifier(saved)
	if err != nil {
		t.Fatalf("BuildNotifier: %v", err)
	}
	if n != nil {
		t.Error("BuildNotifier with neither channel enabled = non-nil, want nil")
	}
}

func TestManager_BuildNotifier_OnlyEnabledChannelsIncluded(t *testing.T) {
	mgr := newTestManager(t)
	ctx := context.Background()

	saved, err := mgr.Save(ctx, SaveParams{
		WebhookEnabled: true, WebhookURL: "https://example.com/hook",
		EmailEnabled: false,
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	n, err := mgr.BuildNotifier(saved)
	if err != nil {
		t.Fatalf("BuildNotifier: %v", err)
	}
	multi, ok := n.(*notify.MultiNotifier)
	if !ok {
		t.Fatalf("BuildNotifier returned %T, want *notify.MultiNotifier", n)
	}
	if len(multi.Notifiers) != 1 {
		t.Fatalf("got %d notifiers, want 1 (only webhook enabled)", len(multi.Notifiers))
	}
	if _, ok := multi.Notifiers[0].(*notify.WebhookNotifier); !ok {
		t.Errorf("notifiers[0] = %T, want *notify.WebhookNotifier", multi.Notifiers[0])
	}
}
