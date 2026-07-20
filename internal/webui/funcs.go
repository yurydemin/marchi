package webui

import (
	"fmt"
	"html/template"
	"strings"
	"time"

	"github.com/yurydemin/marchi/internal/domain"
)

// funcs are available to every parsed page template.
var funcs = template.FuncMap{
	"humanBytes":          humanBytes,
	"formatTime":          formatTime,
	"formatDate":          formatDate,
	"add":                 func(a, b int) int { return a + b },
	"conditionTypes":      conditionTypes,
	"conditionTypeLabel":  conditionTypeLabel,
	"summarizeConditions": summarizeConditions,
	"ruleNodeView":        newRuleNodeView,
}

// RuleNodeView pairs a RuleNode with its depth in the tree (1 = root) —
// the recursive "rule-node" template (rules.html) needs both to decide
// whether to still offer "+ Nested group" (internal/rules.MaxDepth) and
// to set each rendered node's data-depth attribute, which
// web/static/js/app.js reads back when a user adds a sibling/child
// client-side.
type RuleNodeView struct {
	Node  domain.RuleNode
	Depth int
}

func newRuleNodeView(n domain.RuleNode, depth int) RuleNodeView {
	return RuleNodeView{Node: n, Depth: depth}
}

// humanBytes renders n using the same binary (1024-based) units users see
// in most file managers, e.g. 1536 -> "1.5 KiB".
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// formatTime renders *t in a fixed, timezone-explicit format so the
// Dashboard doesn't depend on client-side JS to be readable. Takes a
// pointer since the fields it's fed (e.g. accountStatsResponse.LastSyncAt)
// are themselves optional pointers; callers are expected to have already
// guarded against nil with {{if}}.
func formatTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format("2006-01-02 15:04 MST")
}

// formatDate is formatTime's non-optional counterpart, for fields that
// are always a real time.Time rather than "maybe never happened yet".
func formatDate(t time.Time) string {
	return t.Format("2006-01-02 15:04 MST")
}

// conditionTypes lists FR-RE-02's fifteen leaf condition kinds in a fixed
// order, for populating every condition-type <select> the Rules UI
// renders (the create form's default leaf, and every "+ Condition"
// clone's <template> in app.js — both need the exact same option list, so
// this is the one place it's spelled out).
func conditionTypes() []domain.ConditionType {
	return []domain.ConditionType{
		domain.ConditionFromContains, domain.ConditionFromDomain, domain.ConditionFromExact,
		domain.ConditionToContains, domain.ConditionToDomain,
		domain.ConditionSubjectContains,
		domain.ConditionHasAttachments, domain.ConditionAttachmentType,
		domain.ConditionSizeGreaterThan, domain.ConditionSizeLessThan,
		domain.ConditionDateAfter, domain.ConditionDateBefore,
		domain.ConditionFolderIs, domain.ConditionFolderIsNot,
		domain.ConditionAccountIs,
	}
}

// conditionTypeLabel is conditionTypes' human-readable label, including a
// hint about the expected Value format — internal/rules.validateLeafValue
// is the source of truth for what's actually accepted; this is purely
// descriptive.
func conditionTypeLabel(t domain.ConditionType) string {
	switch t {
	case domain.ConditionFromContains:
		return "From header matches (regex)"
	case domain.ConditionFromDomain:
		return "From domain is"
	case domain.ConditionFromExact:
		return "From address is exactly"
	case domain.ConditionToContains:
		return "To header matches (regex)"
	case domain.ConditionToDomain:
		return "To domain is"
	case domain.ConditionSubjectContains:
		return "Subject matches (regex)"
	case domain.ConditionHasAttachments:
		return "Has attachments (true/false)"
	case domain.ConditionAttachmentType:
		return "Has attachment of type (MIME type)"
	case domain.ConditionSizeGreaterThan:
		return "Size greater than (bytes)"
	case domain.ConditionSizeLessThan:
		return "Size less than (bytes)"
	case domain.ConditionDateAfter:
		return "Date after (e.g. 2026-01-15T00:00:00Z)"
	case domain.ConditionDateBefore:
		return "Date before (e.g. 2026-01-15T00:00:00Z)"
	case domain.ConditionFolderIs:
		return "Folder is"
	case domain.ConditionFolderIsNot:
		return "Folder is not"
	case domain.ConditionAccountIs:
		return "Account ID is"
	default:
		return string(t)
	}
}

// summarizeConditions renders n as a single-line, human-readable summary
// for the rules table's Conditions column — the full tree is only edited
// via the builder (handleRuleRowEdit), this is just enough to recognize a
// rule at a glance without opening it.
func summarizeConditions(n domain.RuleNode) string {
	if n.Op != "" {
		parts := make([]string, len(n.Children))
		for i, c := range n.Children {
			parts[i] = summarizeConditions(c)
		}
		sep := " AND "
		if n.Op == domain.OpOr {
			sep = " OR "
		}
		return "(" + strings.Join(parts, sep) + ")"
	}
	return string(n.Type) + "=\"" + n.Value + "\""
}
