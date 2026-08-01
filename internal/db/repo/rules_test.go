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
	path := filepath.Join(t.TempDir(), "marchi.db")
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
		Action:   domain.ActionArchive,
		IsActive: true,
	}
}

func TestRulesRepo_NewRule_MatchStatsDefaultToZeroAndNeverMatched(t *testing.T) {
	repo := openTestRulesRepo(t)
	ctx := context.Background()

	id, err := repo.Create(ctx, sampleRule())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	rule, err := repo.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if rule.MatchCount != 0 {
		t.Errorf("MatchCount = %d, want 0", rule.MatchCount)
	}
	if !rule.LastMatchedAt.IsZero() {
		t.Errorf("LastMatchedAt = %v, want zero (never matched)", rule.LastMatchedAt)
	}
}

func TestRulesRepo_RecordMatches_AccumulatesAcrossCalls(t *testing.T) {
	repo := openTestRulesRepo(t)
	ctx := context.Background()

	id1, err := repo.Create(ctx, sampleRule())
	if err != nil {
		t.Fatalf("Create rule 1: %v", err)
	}
	rule2 := sampleRule()
	rule2.Name = "Second rule"
	id2, err := repo.Create(ctx, rule2)
	if err != nil {
		t.Fatalf("Create rule 2: %v", err)
	}

	if err := repo.RecordMatches(ctx, map[int64]int{id1: 3, id2: 1}); err != nil {
		t.Fatalf("RecordMatches: %v", err)
	}
	if err := repo.RecordMatches(ctx, map[int64]int{id1: 2}); err != nil {
		t.Fatalf("RecordMatches (second call): %v", err)
	}

	r1, err := repo.GetByID(ctx, id1)
	if err != nil {
		t.Fatalf("GetByID rule 1: %v", err)
	}
	if r1.MatchCount != 5 {
		t.Errorf("rule 1 MatchCount = %d, want 5 (3 + 2 accumulated)", r1.MatchCount)
	}
	if r1.LastMatchedAt.IsZero() {
		t.Error("rule 1 LastMatchedAt was not set")
	}

	r2, err := repo.GetByID(ctx, id2)
	if err != nil {
		t.Fatalf("GetByID rule 2: %v", err)
	}
	if r2.MatchCount != 1 {
		t.Errorf("rule 2 MatchCount = %d, want 1 (untouched by the second call)", r2.MatchCount)
	}
}

func TestRulesRepo_RecordMatches_EmptyMapIsNoop(t *testing.T) {
	repo := openTestRulesRepo(t)
	if err := repo.RecordMatches(context.Background(), nil); err != nil {
		t.Errorf("RecordMatches(nil) = %v, want nil", err)
	}
	if err := repo.RecordMatches(context.Background(), map[int64]int{}); err != nil {
		t.Errorf("RecordMatches({}) = %v, want nil", err)
	}
}

func TestRulesRepo_RecordMatches_UnknownRuleIDIsIgnored(t *testing.T) {
	repo := openTestRulesRepo(t)
	// No rows affected for a nonexistent rule id is not an error —
	// RecordMatches is a best-effort stats update, not a strict write
	// with existence guarantees the caller depends on (unlike, say,
	// UpdateLastUID's callers, which rely on the folder existing).
	if err := repo.RecordMatches(context.Background(), map[int64]int{999999: 1}); err != nil {
		t.Errorf("RecordMatches with an unknown rule id = %v, want nil", err)
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
	if got.CreatedAt.IsZero() {
		t.Error("CreatedAt is zero, want a timestamp")
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
