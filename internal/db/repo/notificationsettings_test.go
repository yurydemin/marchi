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

func openTestNotificationSettingsRepo(t *testing.T) *NotificationSettingsRepo {
	t.Helper()
	path := filepath.Join(t.TempDir(), "marchi.db")
	sqlDB, err := db.Open(path)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })

	w := writer.New(sqlDB)
	t.Cleanup(func() { w.Close() })

	return NewNotificationSettingsRepo(sqlDB, w)
}

func TestNotificationSettingsRepo_Get_NeverConfigured(t *testing.T) {
	repo := openTestNotificationSettingsRepo(t)
	if _, err := repo.Get(context.Background()); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("Get before Upsert = %v, want sql.ErrNoRows", err)
	}
}

func TestNotificationSettingsRepo_UpsertAndGet_RoundTrips(t *testing.T) {
	repo := openTestNotificationSettingsRepo(t)
	ctx := context.Background()

	settings := &domain.NotificationSettings{
		WebhookEnabled: true, WebhookURL: "https://example.com/hook",
		WebhookSecretEncrypted: []byte("encrypted-webhook-secret"),
		EmailEnabled:           true, SMTPHost: "smtp.example.com", SMTPPort: 587,
		SMTPUsername: "notify@example.com", SMTPPasswordEncrypted: []byte("encrypted-smtp-password"),
		SMTPFrom: "notify@example.com", SMTPTo: "admin@example.com",
	}
	if err := repo.Upsert(ctx, settings); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, err := repo.Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.WebhookEnabled || got.WebhookURL != settings.WebhookURL {
		t.Errorf("got webhook = %+v, want matching WebhookEnabled/WebhookURL", got)
	}
	if string(got.WebhookSecretEncrypted) != string(settings.WebhookSecretEncrypted) {
		t.Errorf("WebhookSecretEncrypted = %q, want %q", got.WebhookSecretEncrypted, settings.WebhookSecretEncrypted)
	}
	if !got.EmailEnabled || got.SMTPHost != settings.SMTPHost || got.SMTPPort != settings.SMTPPort {
		t.Errorf("got email = %+v, want matching EmailEnabled/SMTPHost/SMTPPort", got)
	}
	if string(got.SMTPPasswordEncrypted) != string(settings.SMTPPasswordEncrypted) {
		t.Errorf("SMTPPasswordEncrypted = %q, want %q", got.SMTPPasswordEncrypted, settings.SMTPPasswordEncrypted)
	}
	if got.SMTPFrom != settings.SMTPFrom || got.SMTPTo != settings.SMTPTo {
		t.Errorf("got From/To = %q/%q, want %q/%q", got.SMTPFrom, got.SMTPTo, settings.SMTPFrom, settings.SMTPTo)
	}
	if got.UpdatedAt.IsZero() {
		t.Error("UpdatedAt is zero")
	}
}

func TestNotificationSettingsRepo_Upsert_OverwritesExisting(t *testing.T) {
	repo := openTestNotificationSettingsRepo(t)
	ctx := context.Background()

	if err := repo.Upsert(ctx, &domain.NotificationSettings{WebhookEnabled: true, WebhookURL: "https://first.example.com"}); err != nil {
		t.Fatalf("first Upsert: %v", err)
	}
	if err := repo.Upsert(ctx, &domain.NotificationSettings{WebhookEnabled: false, WebhookURL: "https://second.example.com"}); err != nil {
		t.Fatalf("second Upsert: %v", err)
	}

	got, err := repo.Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.WebhookEnabled {
		t.Error("WebhookEnabled = true, want false after second Upsert")
	}
	if got.WebhookURL != "https://second.example.com" {
		t.Errorf("WebhookURL = %q, want the second Upsert's value", got.WebhookURL)
	}
}
