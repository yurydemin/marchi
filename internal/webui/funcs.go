package webui

import (
	"errors"
	"fmt"
	"html/template"
	"strings"
	"time"

	"github.com/yurydemin/marchi/internal/domain"
	"github.com/yurydemin/marchi/internal/i18n"
)

// funcs are available to every parsed page template. "T" and
// "conditionTypeLabel" are locale-dependent — these entries are only
// placeholders so Parse() can validate call sites (name and arg count)
// at parse time; Bind rebinds both to the request's actual *i18n.Localizer
// on a per-request Clone before Execute (see webui.go's doc comment on
// Bind for why Clone, not a fresh Parse, is enough for that).
var funcs = template.FuncMap{
	"humanBytes":          humanBytes,
	"formatTime":          formatTime,
	"formatDate":          formatDate,
	"add":                 func(a, b int) int { return a + b },
	"dict":                dict,
	"conditionTypes":      conditionTypes,
	"conditionTypeLabel":  func(domain.ConditionType) string { return "" },
	"summarizeConditions": summarizeConditions,
	"ruleNodeView":        newRuleNodeView,
	"T":                   func(string, ...i18n.TplData) string { return "" },
	"langs":               func() []string { return i18n.Supported },
}

// dict builds a map from an alternating key/value argument list, letting
// templates pass multiple named placeholders into T for interpolated
// messages (e.g. {{T "dashboard.s3_queue_detail" (dict "Uploading" .X
// "Failed" .Y)}}) — html/template has no map literal syntax of its own.
func dict(pairs ...any) (i18n.TplData, error) {
	if len(pairs)%2 != 0 {
		return nil, errors.New("dict: odd number of arguments")
	}
	d := make(i18n.TplData, len(pairs)/2)
	for i := 0; i < len(pairs); i += 2 {
		key, ok := pairs[i].(string)
		if !ok {
			return nil, fmt.Errorf("dict: key %d must be a string, got %T", i, pairs[i])
		}
		d[key] = pairs[i+1]
	}
	return d, nil
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
// descriptive. Locale-dependent (see Bind), unlike the rest of this
// file's funcs.
func conditionTypeLabel(loc *i18n.Localizer, t domain.ConditionType) string {
	switch t {
	case domain.ConditionFromContains:
		return loc.T("rules.condition.from_contains")
	case domain.ConditionFromDomain:
		return loc.T("rules.condition.from_domain")
	case domain.ConditionFromExact:
		return loc.T("rules.condition.from_exact")
	case domain.ConditionToContains:
		return loc.T("rules.condition.to_contains")
	case domain.ConditionToDomain:
		return loc.T("rules.condition.to_domain")
	case domain.ConditionSubjectContains:
		return loc.T("rules.condition.subject_contains")
	case domain.ConditionHasAttachments:
		return loc.T("rules.condition.has_attachments")
	case domain.ConditionAttachmentType:
		return loc.T("rules.condition.attachment_type")
	case domain.ConditionSizeGreaterThan:
		return loc.T("rules.condition.size_greater_than")
	case domain.ConditionSizeLessThan:
		return loc.T("rules.condition.size_less_than")
	case domain.ConditionDateAfter:
		return loc.T("rules.condition.date_after")
	case domain.ConditionDateBefore:
		return loc.T("rules.condition.date_before")
	case domain.ConditionFolderIs:
		return loc.T("rules.condition.folder_is")
	case domain.ConditionFolderIsNot:
		return loc.T("rules.condition.folder_is_not")
	case domain.ConditionAccountIs:
		return loc.T("rules.condition.account_is")
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
