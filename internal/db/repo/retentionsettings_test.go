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

func openTestRetentionSettingsRepo(t *testing.T) *RetentionSettingsRepo {
	t.Helper()
	path := filepath.Join(t.TempDir(), "marchi.db")
	sqlDB, err := db.Open(path)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })

	w := writer.New(sqlDB)
	t.Cleanup(func() { w.Close() })

	return NewRetentionSettingsRepo(sqlDB, w)
}

func TestRetentionSettingsRepo_Get_NeverConfigured(t *testing.T) {
	repo := openTestRetentionSettingsRepo(t)
	if _, err := repo.Get(context.Background()); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("Get before Upsert = %v, want sql.ErrNoRows", err)
	}
}

func TestRetentionSettingsRepo_UpsertAndGet_RoundTrips(t *testing.T) {
	repo := openTestRetentionSettingsRepo(t)
	ctx := context.Background()

	settings := &domain.RetentionSettings{
		DefaultLocalDays: intPtr(60), DefaultMoveToS3Days: intPtr(30), DefaultS3Days: intPtr(2555),
	}
	if err := repo.Upsert(ctx, settings); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, err := repo.Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.DefaultLocalDays == nil || *got.DefaultLocalDays != 60 {
		t.Errorf("DefaultLocalDays = %v, want 60", got.DefaultLocalDays)
	}
	if got.DefaultMoveToS3Days == nil || *got.DefaultMoveToS3Days != 30 {
		t.Errorf("DefaultMoveToS3Days = %v, want 30", got.DefaultMoveToS3Days)
	}
	if got.DefaultS3Days == nil || *got.DefaultS3Days != 2555 {
		t.Errorf("DefaultS3Days = %v, want 2555", got.DefaultS3Days)
	}
	if got.UpdatedAt.IsZero() {
		t.Error("UpdatedAt is zero, want a timestamp")
	}
}

func TestRetentionSettingsRepo_Upsert_OverwritesSingletonRow(t *testing.T) {
	repo := openTestRetentionSettingsRepo(t)
	ctx := context.Background()

	if err := repo.Upsert(ctx, &domain.RetentionSettings{DefaultLocalDays: intPtr(10)}); err != nil {
		t.Fatalf("first Upsert: %v", err)
	}
	if err := repo.Upsert(ctx, &domain.RetentionSettings{DefaultLocalDays: intPtr(20)}); err != nil {
		t.Fatalf("second Upsert: %v", err)
	}

	got, err := repo.Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.DefaultLocalDays == nil || *got.DefaultLocalDays != 20 {
		t.Errorf("DefaultLocalDays = %v, want 20 (second Upsert's value, no duplicate row)", got.DefaultLocalDays)
	}
}

func TestRetentionSettingsRepo_Upsert_NilFieldsStayNil(t *testing.T) {
	repo := openTestRetentionSettingsRepo(t)
	ctx := context.Background()

	if err := repo.Upsert(ctx, &domain.RetentionSettings{DefaultS3Days: intPtr(2555)}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, err := repo.Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.DefaultLocalDays != nil || got.DefaultMoveToS3Days != nil {
		t.Errorf("got = %+v, want DefaultLocalDays/DefaultMoveToS3Days nil (never set)", got)
	}
}
