package rules

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/yurydemin/marchi/internal/db"
	"github.com/yurydemin/marchi/internal/db/repo"
	"github.com/yurydemin/marchi/internal/db/writer"
	"github.com/yurydemin/marchi/internal/domain"
)

func openTestRulesRepo(t *testing.T) *repo.RulesRepo {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mailvault.db")
	sqlDB, err := db.Open(path)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })

	w := writer.New(sqlDB)
	t.Cleanup(func() { w.Close() })

	return repo.NewRulesRepo(sqlDB, w)
}

const validYAML = `
rules:
  - name: skip newsletters
    priority: 0
    action: skip
    is_active: true
    conditions:
      type: subject_contains
      value: "(?i)newsletter"
  - name: vendor invoices
    priority: 10
    action: archive
    retention_local_days: 30
    retention_move_to_s3_days: 7
    retention_s3_days: 2555
    conditions:
      op: and
      children:
        - type: from_domain
          value: vendor.com
        - type: subject_contains
          value: "(?i)invoice"
`

func TestParseYAML_ValidFile(t *testing.T) {
	rulesList, err := ParseYAML([]byte(validYAML))
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}
	if len(rulesList) != 2 {
		t.Fatalf("got %d rules, want 2", len(rulesList))
	}

	first := rulesList[0]
	if first.Name != "skip newsletters" || first.Action != domain.ActionSkip || !first.IsActive {
		t.Errorf("first rule = %+v", first)
	}
	if first.Conditions.Type != domain.ConditionSubjectContains || first.Conditions.Value != "(?i)newsletter" {
		t.Errorf("first rule conditions = %+v", first.Conditions)
	}

	second := rulesList[1]
	if second.Name != "vendor invoices" || second.Action != domain.ActionArchive {
		t.Errorf("second rule = %+v", second)
	}
	if second.Conditions.Op != domain.OpAnd || len(second.Conditions.Children) != 2 {
		t.Fatalf("second rule conditions = %+v", second.Conditions)
	}
	if second.RetentionLocalDays == nil || *second.RetentionLocalDays != 30 {
		t.Errorf("RetentionLocalDays = %v, want 30", second.RetentionLocalDays)
	}
	if second.RetentionMoveToS3Days == nil || *second.RetentionMoveToS3Days != 7 {
		t.Errorf("RetentionMoveToS3Days = %v, want 7", second.RetentionMoveToS3Days)
	}
	if second.RetentionS3Days == nil || *second.RetentionS3Days != 2555 {
		t.Errorf("RetentionS3Days = %v, want 2555", second.RetentionS3Days)
	}
}

func TestParseYAML_RejectsMissingName(t *testing.T) {
	yamlData := `
rules:
  - priority: 0
    action: archive
    conditions: {type: folder_is, value: INBOX}
`
	if _, err := ParseYAML([]byte(yamlData)); err == nil {
		t.Error("ParseYAML should reject a rule with no name")
	}
}

func TestParseYAML_RejectsInvalidAction(t *testing.T) {
	yamlData := `
rules:
  - name: bad action
    action: delete_everything
    conditions: {type: folder_is, value: INBOX}
`
	if _, err := ParseYAML([]byte(yamlData)); err == nil {
		t.Error("ParseYAML should reject an unknown action")
	}
}

func TestParseYAML_RejectsInvalidConditions(t *testing.T) {
	yamlData := `
rules:
  - name: bad regex
    action: archive
    conditions: {type: subject_contains, value: "("}
`
	if _, err := ParseYAML([]byte(yamlData)); err == nil {
		t.Error("ParseYAML should reject a rule whose conditions fail Validate")
	}
}

func TestParseYAML_MalformedYAML(t *testing.T) {
	if _, err := ParseYAML([]byte("not: valid: yaml: [")); err == nil {
		t.Error("ParseYAML should reject malformed YAML")
	}
}

func TestSyncYAML_CreatesNewRules(t *testing.T) {
	rulesRepo := openTestRulesRepo(t)
	ctx := context.Background()

	parsed, err := ParseYAML([]byte(validYAML))
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}
	if err := SyncYAML(ctx, rulesRepo, parsed); err != nil {
		t.Fatalf("SyncYAML: %v", err)
	}

	stored, err := rulesRepo.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(stored) != 2 {
		t.Fatalf("got %d stored rules, want 2", len(stored))
	}
}

func TestSyncYAML_UpdatesExistingRuleByName(t *testing.T) {
	rulesRepo := openTestRulesRepo(t)
	ctx := context.Background()

	id, err := rulesRepo.Create(ctx, &domain.Rule{
		Name: "skip newsletters", Priority: 99,
		Conditions: domain.RuleNode{Type: domain.ConditionFolderIs, Value: "Spam"},
		Action:     domain.ActionArchive, IsActive: false,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	parsed, err := ParseYAML([]byte(validYAML))
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}
	if err := SyncYAML(ctx, rulesRepo, parsed); err != nil {
		t.Fatalf("SyncYAML: %v", err)
	}

	updated, err := rulesRepo.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if updated.Priority != 0 || updated.Action != domain.ActionSkip || !updated.IsActive {
		t.Errorf("existing rule wasn't updated in place: %+v", updated)
	}
	if updated.Conditions.Type != domain.ConditionSubjectContains {
		t.Errorf("existing rule's conditions weren't updated: %+v", updated.Conditions)
	}

	stored, err := rulesRepo.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(stored) != 2 {
		t.Fatalf("got %d stored rules, want 2 (1 updated + 1 newly created, no duplicate)", len(stored))
	}
}

// TestSyncYAML_NeverDeletesRulesAbsentFromFile is the load-bearing safety
// property documented on SyncYAML: a rule created through some other path
// (Web UI/REST, a previous YAML sync) must survive a sync from a YAML
// file that doesn't mention it.
func TestSyncYAML_NeverDeletesRulesAbsentFromFile(t *testing.T) {
	rulesRepo := openTestRulesRepo(t)
	ctx := context.Background()

	uiCreatedID, err := rulesRepo.Create(ctx, &domain.Rule{
		Name: "created via UI, not in any YAML file", Priority: 5,
		Conditions: domain.RuleNode{Type: domain.ConditionHasAttachments, Value: "true"},
		Action:     domain.ActionArchive, IsActive: true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	parsed, err := ParseYAML([]byte(validYAML))
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}
	if err := SyncYAML(ctx, rulesRepo, parsed); err != nil {
		t.Fatalf("SyncYAML: %v", err)
	}

	if _, err := rulesRepo.GetByID(ctx, uiCreatedID); err != nil {
		t.Errorf("UI-created rule was removed by SyncYAML: %v", err)
	}
	stored, err := rulesRepo.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(stored) != 3 {
		t.Fatalf("got %d stored rules, want 3 (1 pre-existing + 2 from YAML)", len(stored))
	}
}

func TestLoadYAMLFile_MissingFileIsNotAnError(t *testing.T) {
	rulesRepo := openTestRulesRepo(t)
	loaded, err := LoadYAMLFile(context.Background(), filepath.Join(t.TempDir(), "does-not-exist.yaml"), rulesRepo)
	if err != nil {
		t.Fatalf("LoadYAMLFile: %v", err)
	}
	if loaded {
		t.Error("loaded = true, want false for a missing file")
	}
}
