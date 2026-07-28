package repo

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/yurydemin/marchi/internal/db"
	"github.com/yurydemin/marchi/internal/db/writer"
	"github.com/yurydemin/marchi/internal/domain"
)

func openTestAuditLogRepo(t *testing.T) *AuditLogRepo {
	t.Helper()
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "marchi.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	w := writer.New(sqlDB)
	t.Cleanup(func() { w.Close() })

	return NewAuditLogRepo(sqlDB, w)
}

func TestAuditLogRepo_InsertThenList_NewestFirst(t *testing.T) {
	r := openTestAuditLogRepo(t)
	ctx := context.Background()

	if err := r.Insert(ctx, domain.AuditEventUnlock, "127.0.0.1", "Vault unlocked via web session"); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := r.Insert(ctx, domain.AuditEventRuleDelete, "", "Deleted rule #1 \"Skip junk\""); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	entries, err := r.List(ctx, 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}

	// Newest first: the rule deletion (inserted second) must come before
	// the unlock (inserted first).
	if entries[0].EventType != domain.AuditEventRuleDelete {
		t.Errorf("entries[0].EventType = %q, want %q", entries[0].EventType, domain.AuditEventRuleDelete)
	}
	if entries[0].IP != "" {
		t.Errorf("entries[0].IP = %q, want empty (a CLI-triggered-style event with no IP)", entries[0].IP)
	}
	if entries[1].EventType != domain.AuditEventUnlock || entries[1].IP != "127.0.0.1" {
		t.Errorf("entries[1] = %+v, want the unlock event with its IP intact", entries[1])
	}
	if entries[0].CreatedAt.IsZero() || entries[1].CreatedAt.IsZero() {
		t.Error("CreatedAt should be stamped by the database, not left zero")
	}
}

func TestAuditLogRepo_List_RespectsLimit(t *testing.T) {
	r := openTestAuditLogRepo(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		if err := r.Insert(ctx, domain.AuditEventEmailDelete, "10.0.0.1", "test entry"); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}

	entries, err := r.List(ctx, 3)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3 (limit should cap the result)", len(entries))
	}
}
