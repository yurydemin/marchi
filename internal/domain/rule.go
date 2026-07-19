package domain

import (
	"fmt"
	"time"
)

// LogicalOp combines a RuleNode group's Children (FR-RE-02: AND/OR logic).
type LogicalOp string

const (
	OpAnd LogicalOp = "and"
	OpOr  LogicalOp = "or"
)

// ConditionType is one of FR-RE-02's fifteen leaf condition kinds.
type ConditionType string

const (
	ConditionFromContains    ConditionType = "from_contains"
	ConditionFromDomain      ConditionType = "from_domain"
	ConditionFromExact       ConditionType = "from_exact"
	ConditionToContains      ConditionType = "to_contains"
	ConditionToDomain        ConditionType = "to_domain"
	ConditionSubjectContains ConditionType = "subject_contains"
	ConditionHasAttachments  ConditionType = "has_attachments"
	ConditionAttachmentType  ConditionType = "attachment_type"
	ConditionSizeGreaterThan ConditionType = "size_greater_than"
	ConditionSizeLessThan    ConditionType = "size_less_than"
	ConditionDateAfter       ConditionType = "date_after"
	ConditionDateBefore      ConditionType = "date_before"
	ConditionFolderIs        ConditionType = "folder_is"
	ConditionFolderIsNot     ConditionType = "folder_is_not"
	ConditionAccountIs       ConditionType = "account_is"
)

// RuleNode is one node of a rule's condition tree (FR-RE-02): either a
// group (Op set, combining Children with AND/OR) or a leaf (Type set,
// testing one property of a candidate message against Value). Exactly one
// of the two must be populated, and a tree may nest groups up to 3 levels
// deep — internal/rules.Validate enforces both, since that's where the
// evaluation semantics live, not here (this package stays behavior-free).
//
// Value is always a string regardless of the condition's actual type
// (regex pattern, plain string, "true"/"false", byte count, ISO 8601
// date, or account id) — keeping the tree's JSON shape uniform; each
// condition type parses Value itself at evaluation time.
//
// Tagged for both JSON (Rules REST API, conditions_json storage) and YAML
// (FR-RE-05's rules.yaml) — the two use identical field names by design,
// so a rule round-trips the same shape through either surface.
type RuleNode struct {
	Op       LogicalOp  `json:"op,omitempty" yaml:"op,omitempty"`
	Children []RuleNode `json:"children,omitempty" yaml:"children,omitempty"`

	Type  ConditionType `json:"type,omitempty" yaml:"type,omitempty"`
	Value string        `json:"value,omitempty" yaml:"value,omitempty"`
}

// RuleAction is FR-RE-03's four possible outcomes for a rule whose
// conditions matched. Rules are evaluated in Priority order (ascending —
// lower Priority runs first) and the first match wins; see
// internal/rules.FirstMatch.
type RuleAction string

const (
	ActionArchive            RuleAction = "archive"
	ActionSkip               RuleAction = "skip"
	ActionArchiveAndDelete   RuleAction = "archive_and_delete"
	ActionArchiveAndMarkRead RuleAction = "archive_and_mark_read"
)

// ParseRuleAction is String's inverse for RuleAction — shared by the
// Rules REST API and CLI/YAML loading so they never drift on which
// strings are accepted, matching the ParseIMAPTLSMode precedent.
func ParseRuleAction(s string) (RuleAction, error) {
	switch RuleAction(s) {
	case ActionArchive, ActionSkip, ActionArchiveAndDelete, ActionArchiveAndMarkRead:
		return RuleAction(s), nil
	default:
		return "", fmt.Errorf("invalid rule action %q (want archive, skip, archive_and_delete, or archive_and_mark_read)", s)
	}
}

// Rule is an archive rule (FR-RE-01..05). Conditions gates whether/how a
// fetched-but-not-yet-archived message is handled (internal/rules.FirstMatch
// picks the winning Rule; internal/sync's archiveOne applies its Action).
//
// Retention is deliberately not a per-rule setting (see
// internal/retention's package doc): it lives on a global default
// (RetentionSettings) with an optional per-account override, both
// evaluated fresh at retention-cron time rather than snapshotted from
// whichever rule happened to archive a given email.
//
// ID and CreatedAt are database-assigned and excluded from YAML (a
// rules.yaml file identifies a rule by Name — see internal/rules.SyncYAML).
type Rule struct {
	ID         int64      `yaml:"-"`
	Name       string     `yaml:"name"`
	Priority   int        `yaml:"priority"`
	Conditions RuleNode   `yaml:"conditions"`
	Action     RuleAction `yaml:"action"`
	IsActive   bool       `yaml:"is_active"`
	CreatedAt  time.Time  `yaml:"-"`
}
