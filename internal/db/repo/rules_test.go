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

func openTestRulesRepo(t *testing.T) *RulesRepo {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mailvault.db")
	sqlDB, err := db.Open(path)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })

	w := writer.New(sqlDB)
	t.Cleanup(func() { w.Close() })

	return NewRulesRepo(sqlDB, w)
}

func intPtr(n int) *int { return &n }

func sampleRule() *domain.Rule {
	return &domain.Rule{
		Name:     "Archive invoices",
		Priority: 10,
		Conditions: domain.RuleNode{
			Op: domain.OpAnd,
			Children: []domain.RuleNode{
				{Type: domain.ConditionFromDomain, Value: "vendor.com"},
				{Type: domain.ConditionSubjectContains, Value: "(?i)invoice"},
			},
		},
		Action:                domain.ActionArchive,
		RetentionLocalDays:    intPtr(30),
		RetentionMoveToS3Days: intPtr(7),
		RetentionS3Days:       intPtr(2555),
		IsActive:              true,
	}
}

func TestRulesRepo_CreateAndGetByID_RoundTripsConditionsTree(t *testing.T) {
	repo := openTestRulesRepo(t)
	ctx := context.Background()

	rule := sampleRule()
	id, err := repo.Create(ctx, rule)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if id == 0 {
		t.Fatal("Create returned id=0")
	}

	got, err := repo.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Name != rule.Name || got.Priority != rule.Priority || got.Action != rule.Action {
		t.Errorf("got = %+v, want matching Name/Priority/Action from %+v", got, rule)
	}
	if got.Conditions.Op != domain.OpAnd || len(got.Conditions.Children) != 2 {
		t.Fatalf("Conditions tree didn't round-trip: %+v", got.Conditions)
	}
	if got.Conditions.Children[0].Type != domain.ConditionFromDomain || got.Conditions.Children[0].Value != "vendor.com" {
		t.Errorf("Conditions.Children[0] = %+v, want from_domain=vendor.com", got.Conditions.Children[0])
	}
	if got.RetentionLocalDays == nil || *got.RetentionLocalDays != 30 {
		t.Errorf("RetentionLocalDays = %v, want 30", got.RetentionLocalDays)
	}
	if got.RetentionMoveToS3Days == nil || *got.RetentionMoveToS3Days != 7 {
		t.Errorf("RetentionMoveToS3Days = %v, want 7", got.RetentionMoveToS3Days)
	}
	if got.RetentionS3Days == nil || *got.RetentionS3Days != 2555 {
		t.Errorf("RetentionS3Days = %v, want 2555", got.RetentionS3Days)
	}
	if got.CreatedAt.IsZero() {
		t.Error("CreatedAt is zero, want a timestamp")
	}
}

func TestRulesRepo_Create_NilRetentionFieldsStayNil(t *testing.T) {
	repo := openTestRulesRepo(t)
	ctx := context.Background()

	rule := &domain.Rule{
		Name:       "Skip newsletters",
		Priority:   5,
		Conditions: domain.RuleNode{Type: domain.ConditionSubjectContains, Value: "(?i)newsletter"},
		Action:     domain.ActionSkip,
		IsActive:   true,
	}
	id, err := repo.Create(ctx, rule)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := repo.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.RetentionLocalDays != nil || got.RetentionMoveToS3Days != nil || got.RetentionS3Days != nil {
		t.Errorf("expected all retention fields nil, got %+v", got)
	}
}

func TestRulesRepo_List_OrderedByPriorityThenID(t *testing.T) {
	repo := openTestRulesRepo(t)
	ctx := context.Background()

	mustCreate := func(name string, priority int) int64 {
		id, err := repo.Create(ctx, &domain.Rule{
			Name: name, Priority: priority,
			Conditions: domain.RuleNode{Type: domain.ConditionHasAttachments, Value: "true"},
			Action:     domain.ActionArchive, IsActive: true,
		})
		if err != nil {
			t.Fatalf("Create(%s): %v", name, err)
		}
		return id
	}
	mustCreate("second-priority-10-a", 10)
	mustCreate("first-priority-0", 0)
	mustCreate("third-priority-10-b", 10)

	rules, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rules) != 3 {
		t.Fatalf("List returned %d rules, want 3", len(rules))
	}
	want := []string{"first-priority-0", "second-priority-10-a", "third-priority-10-b"}
	for i, name := range want {
		if rules[i].Name != name {
			t.Errorf("rules[%d].Name = %q, want %q", i, rules[i].Name, name)
		}
	}
}

func TestRulesRepo_ListActive_ExcludesInactiveRules(t *testing.T) {
	repo := openTestRulesRepo(t)
	ctx := context.Background()

	active, err := repo.Create(ctx, &domain.Rule{
		Name: "active", Priority: 0,
		Conditions: domain.RuleNode{Type: domain.ConditionHasAttachments, Value: "true"},
		Action:     domain.ActionArchive, IsActive: true,
	})
	if err != nil {
		t.Fatalf("Create(active): %v", err)
	}
	if _, err := repo.Create(ctx, &domain.Rule{
		Name: "inactive", Priority: 1,
		Conditions: domain.RuleNode{Type: domain.ConditionHasAttachments, Value: "true"},
		Action:     domain.ActionSkip, IsActive: false,
	}); err != nil {
		t.Fatalf("Create(inactive): %v", err)
	}

	rules, err := repo.ListActive(ctx)
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	if len(rules) != 1 || rules[0].ID != active {
		t.Fatalf("ListActive = %+v, want only the active rule (id=%d)", rules, active)
	}
}

func TestRulesRepo_Update(t *testing.T) {
	repo := openTestRulesRepo(t)
	ctx := context.Background()

	rule := sampleRule()
	id, err := repo.Create(ctx, rule)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	rule.ID = id
	rule.Name = "Renamed"
	rule.Priority = 99
	rule.Action = domain.ActionSkip
	rule.Conditions = domain.RuleNode{Type: domain.ConditionAccountIs, Value: "1"}
	rule.RetentionLocalDays = nil
	if err := repo.Update(ctx, rule); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := repo.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Name != "Renamed" || got.Priority != 99 || got.Action != domain.ActionSkip {
		t.Errorf("got = %+v, want updated Name/Priority/Action", got)
	}
	if got.Conditions.Type != domain.ConditionAccountIs || got.Conditions.Value != "1" {
		t.Errorf("Conditions = %+v, want updated leaf", got.Conditions)
	}
	if got.RetentionLocalDays != nil {
		t.Errorf("RetentionLocalDays = %v, want nil after update", got.RetentionLocalDays)
	}
}

func TestRulesRepo_Update_UnknownID(t *testing.T) {
	repo := openTestRulesRepo(t)
	rule := sampleRule()
	rule.ID = 999
	if err := repo.Update(context.Background(), rule); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("Update(unknown id) = %v, want sql.ErrNoRows", err)
	}
}

func TestRulesRepo_Delete(t *testing.T) {
	repo := openTestRulesRepo(t)
	ctx := context.Background()

	id, err := repo.Create(ctx, sampleRule())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := repo.Delete(ctx, id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := repo.GetByID(ctx, id); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("GetByID after Delete = %v, want sql.ErrNoRows", err)
	}
}

func TestRulesRepo_Delete_UnknownID(t *testing.T) {
	repo := openTestRulesRepo(t)
	if err := repo.Delete(context.Background(), 999); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("Delete(unknown id) = %v, want sql.ErrNoRows", err)
	}
}

func TestRulesRepo_GetByID_UnknownID(t *testing.T) {
	repo := openTestRulesRepo(t)
	if _, err := repo.GetByID(context.Background(), 999); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("GetByID(unknown id) = %v, want sql.ErrNoRows", err)
	}
}
