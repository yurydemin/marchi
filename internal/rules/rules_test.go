package rules

import (
	"testing"
	"time"

	"github.com/yurydemin/marchi/internal/domain"
)

func leaf(t domain.ConditionType, value string) domain.RuleNode {
	return domain.RuleNode{Type: t, Value: value}
}

func TestEvaluate_EachConditionType(t *testing.T) {
	base := Candidate{
		From:            `"Billing" <billing@vendor.com>`,
		FromAddr:        "billing@vendor.com",
		To:              []string{`"Me" <me@example.com>`},
		ToAddrs:         []string{"me@example.com"},
		Cc:              nil,
		CcAddrs:         nil,
		Subject:         "Your July Invoice",
		HasAttachments:  true,
		AttachmentTypes: []string{"application/pdf"},
		Size:            5000,
		Date:            time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC),
		FolderName:      "INBOX",
		AccountID:       42,
	}

	tests := []struct {
		name string
		node domain.RuleNode
		want bool
	}{
		{"from_contains match", leaf(domain.ConditionFromContains, "(?i)billing"), true},
		{"from_contains no match", leaf(domain.ConditionFromContains, "(?i)nobody"), false},
		{"from_domain match", leaf(domain.ConditionFromDomain, "vendor.com"), true},
		{"from_domain case-insensitive", leaf(domain.ConditionFromDomain, "VENDOR.COM"), true},
		{"from_domain no match", leaf(domain.ConditionFromDomain, "other.com"), false},
		{"from_exact match", leaf(domain.ConditionFromExact, "billing@vendor.com"), true},
		{"from_exact no match", leaf(domain.ConditionFromExact, "someone@vendor.com"), false},
		{"to_contains match", leaf(domain.ConditionToContains, "(?i)me@"), true},
		{"to_contains no match", leaf(domain.ConditionToContains, "(?i)nope@"), false},
		{"to_domain match", leaf(domain.ConditionToDomain, "example.com"), true},
		{"to_domain no match", leaf(domain.ConditionToDomain, "other.com"), false},
		{"subject_contains match", leaf(domain.ConditionSubjectContains, "(?i)invoice"), true},
		{"subject_contains no match", leaf(domain.ConditionSubjectContains, "(?i)newsletter"), false},
		{"has_attachments true match", leaf(domain.ConditionHasAttachments, "true"), true},
		{"has_attachments false no-match", leaf(domain.ConditionHasAttachments, "false"), false},
		{"attachment_type match", leaf(domain.ConditionAttachmentType, "application/pdf"), true},
		{"attachment_type no match", leaf(domain.ConditionAttachmentType, "image/png"), false},
		{"size_greater_than match", leaf(domain.ConditionSizeGreaterThan, "1000"), true},
		{"size_greater_than no match", leaf(domain.ConditionSizeGreaterThan, "10000"), false},
		{"size_less_than match", leaf(domain.ConditionSizeLessThan, "10000"), true},
		{"size_less_than no match", leaf(domain.ConditionSizeLessThan, "1000"), false},
		{"date_after match", leaf(domain.ConditionDateAfter, "2026-01-01T00:00:00Z"), true},
		{"date_after no match", leaf(domain.ConditionDateAfter, "2027-01-01T00:00:00Z"), false},
		{"date_before match", leaf(domain.ConditionDateBefore, "2027-01-01T00:00:00Z"), true},
		{"date_before no match", leaf(domain.ConditionDateBefore, "2026-01-01T00:00:00Z"), false},
		{"folder_is match", leaf(domain.ConditionFolderIs, "INBOX"), true},
		{"folder_is no match", leaf(domain.ConditionFolderIs, "Archive"), false},
		{"folder_is_not match", leaf(domain.ConditionFolderIsNot, "Archive"), true},
		{"folder_is_not no match", leaf(domain.ConditionFolderIsNot, "INBOX"), false},
		{"account_is match", leaf(domain.ConditionAccountIs, "42"), true},
		{"account_is no match", leaf(domain.ConditionAccountIs, "7"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Evaluate(tt.node, base); got != tt.want {
				t.Errorf("Evaluate(%+v) = %v, want %v", tt.node, got, tt.want)
			}
		})
	}
}

func TestEvaluate_AndRequiresAllChildren(t *testing.T) {
	c := Candidate{FromAddr: "a@example.com", Subject: "hello", AccountID: 1}
	and := domain.RuleNode{Op: domain.OpAnd, Children: []domain.RuleNode{
		leaf(domain.ConditionFromDomain, "example.com"),
		leaf(domain.ConditionSubjectContains, "(?i)hello"),
	}}
	if !Evaluate(and, c) {
		t.Error("AND of two true leaves should be true")
	}

	andFail := domain.RuleNode{Op: domain.OpAnd, Children: []domain.RuleNode{
		leaf(domain.ConditionFromDomain, "example.com"),
		leaf(domain.ConditionSubjectContains, "(?i)goodbye"),
	}}
	if Evaluate(andFail, c) {
		t.Error("AND with one false leaf should be false")
	}
}

func TestEvaluate_OrRequiresAnyChild(t *testing.T) {
	c := Candidate{FromAddr: "a@example.com", Subject: "hello"}
	or := domain.RuleNode{Op: domain.OpOr, Children: []domain.RuleNode{
		leaf(domain.ConditionFromDomain, "nope.com"),
		leaf(domain.ConditionSubjectContains, "(?i)hello"),
	}}
	if !Evaluate(or, c) {
		t.Error("OR with one true leaf should be true")
	}

	orFail := domain.RuleNode{Op: domain.OpOr, Children: []domain.RuleNode{
		leaf(domain.ConditionFromDomain, "nope.com"),
		leaf(domain.ConditionSubjectContains, "(?i)goodbye"),
	}}
	if Evaluate(orFail, c) {
		t.Error("OR with all false leaves should be false")
	}
}

func TestEvaluate_NestedThreeLevels(t *testing.T) {
	// (from_domain=vendor.com AND (subject_contains=invoice OR subject_contains=receipt))
	c := Candidate{FromAddr: "x@vendor.com", Subject: "Your receipt"}
	tree := domain.RuleNode{Op: domain.OpAnd, Children: []domain.RuleNode{
		leaf(domain.ConditionFromDomain, "vendor.com"),
		{Op: domain.OpOr, Children: []domain.RuleNode{
			leaf(domain.ConditionSubjectContains, "(?i)invoice"),
			leaf(domain.ConditionSubjectContains, "(?i)receipt"),
		}},
	}}
	if !Evaluate(tree, c) {
		t.Error("nested AND(domain, OR(invoice, receipt)) should match a receipt from vendor.com")
	}

	c.Subject = "Something else"
	if Evaluate(tree, c) {
		t.Error("nested tree should not match once neither OR branch matches")
	}
}

func TestValidate_RejectsNodeWithBothOrNeitherOpAndType(t *testing.T) {
	both := domain.RuleNode{Op: domain.OpAnd, Type: domain.ConditionFolderIs, Value: "INBOX"}
	if err := Validate(both); err == nil {
		t.Error("Validate should reject a node with both op and type set")
	}

	neither := domain.RuleNode{}
	if err := Validate(neither); err == nil {
		t.Error("Validate should reject a node with neither op nor type set")
	}
}

func TestValidate_RejectsEmptyGroup(t *testing.T) {
	empty := domain.RuleNode{Op: domain.OpAnd}
	if err := Validate(empty); err == nil {
		t.Error("Validate should reject a group with no children")
	}
}

func TestValidate_RejectsUnknownOpAndType(t *testing.T) {
	if err := Validate(domain.RuleNode{Op: "xor", Children: []domain.RuleNode{leaf(domain.ConditionFolderIs, "x")}}); err == nil {
		t.Error("Validate should reject an unknown op")
	}
	if err := Validate(domain.RuleNode{Type: "smells_fishy", Value: "x"}); err == nil {
		t.Error("Validate should reject an unknown condition type")
	}
}

func TestValidate_RejectsInvalidLeafValues(t *testing.T) {
	tests := []domain.RuleNode{
		leaf(domain.ConditionFromContains, "("), // invalid regex
		leaf(domain.ConditionHasAttachments, "maybe"),
		leaf(domain.ConditionSizeGreaterThan, "not-a-number"),
		leaf(domain.ConditionDateAfter, "not-a-date"),
		leaf(domain.ConditionAccountIs, "not-an-id"),
		leaf(domain.ConditionFromDomain, ""),
	}
	for _, n := range tests {
		if err := Validate(n); err == nil {
			t.Errorf("Validate(%+v) should have failed", n)
		}
	}
}

func TestValidate_AcceptsMaxDepthThree(t *testing.T) {
	// Depth 1 (root AND) -> depth 2 (nested OR) -> depth 3 (leaves). Legal.
	tree := domain.RuleNode{Op: domain.OpAnd, Children: []domain.RuleNode{
		leaf(domain.ConditionFolderIs, "INBOX"),
		{Op: domain.OpOr, Children: []domain.RuleNode{
			leaf(domain.ConditionSubjectContains, "a"),
			leaf(domain.ConditionSubjectContains, "b"),
		}},
	}}
	if err := Validate(tree); err != nil {
		t.Errorf("Validate should accept a 3-level-deep tree: %v", err)
	}
}

func TestValidate_RejectsDepthFour(t *testing.T) {
	// Root AND (L1) -> OR (L2) -> AND (L3) -> OR (L4, one group level too
	// many) -> leaf. MaxDepth caps nested *groups* at 3 levels; leaves are
	// always terminal and don't themselves count toward the limit (see
	// TestValidate_AcceptsMaxDepthThree, whose 3rd level is a group whose
	// children are plain leaves).
	tree := domain.RuleNode{Op: domain.OpAnd, Children: []domain.RuleNode{
		{Op: domain.OpOr, Children: []domain.RuleNode{
			{Op: domain.OpAnd, Children: []domain.RuleNode{
				{Op: domain.OpOr, Children: []domain.RuleNode{
					leaf(domain.ConditionFolderIs, "INBOX"),
				}},
			}},
		}},
	}}
	if err := Validate(tree); err == nil {
		t.Error("Validate should reject a tree nested 4 group-levels deep")
	}
}

func TestFirstMatch_ReturnsFirstMatchingRuleInPriorityOrder(t *testing.T) {
	c := Candidate{FromAddr: "billing@vendor.com", Subject: "Invoice #42"}
	rulesList := []*domain.Rule{
		{ID: 1, Name: "catch-all", Conditions: leaf(domain.ConditionFolderIs, "INBOX"), Action: domain.ActionArchive},
		{ID: 2, Name: "vendor-specific", Conditions: leaf(domain.ConditionFromDomain, "vendor.com"), Action: domain.ActionSkip},
	}
	// c.FolderName is "" so the first rule doesn't match; the second does.
	got := FirstMatch(rulesList, c)
	if got == nil || got.ID != 2 {
		t.Fatalf("FirstMatch = %+v, want rule id=2", got)
	}
}

func TestFirstMatch_NoMatchReturnsNil(t *testing.T) {
	c := Candidate{FromAddr: "nobody@nowhere.com"}
	rulesList := []*domain.Rule{
		{ID: 1, Conditions: leaf(domain.ConditionFromDomain, "vendor.com")},
	}
	if got := FirstMatch(rulesList, c); got != nil {
		t.Errorf("FirstMatch = %+v, want nil", got)
	}
}

func TestFirstMatch_EmptyRuleListReturnsNil(t *testing.T) {
	if got := FirstMatch(nil, Candidate{}); got != nil {
		t.Errorf("FirstMatch(nil) = %+v, want nil", got)
	}
}

// Sanity check that the whole package is import-clean and Evaluate/regex
// helpers don't panic on empty/zero Candidate fields.
func TestEvaluate_ZeroCandidateDoesNotPanic(t *testing.T) {
	for _, ct := range []domain.ConditionType{
		domain.ConditionFromContains, domain.ConditionFromDomain, domain.ConditionFromExact,
		domain.ConditionToContains, domain.ConditionToDomain, domain.ConditionSubjectContains,
		domain.ConditionHasAttachments, domain.ConditionAttachmentType,
		domain.ConditionSizeGreaterThan, domain.ConditionSizeLessThan,
		domain.ConditionDateAfter, domain.ConditionDateBefore,
		domain.ConditionFolderIs, domain.ConditionFolderIsNot, domain.ConditionAccountIs,
	} {
		value := "true"
		switch ct {
		case domain.ConditionSizeGreaterThan, domain.ConditionSizeLessThan, domain.ConditionAccountIs:
			value = "0"
		case domain.ConditionDateAfter, domain.ConditionDateBefore:
			value = "2026-01-01T00:00:00Z"
		case domain.ConditionFromContains, domain.ConditionToContains, domain.ConditionSubjectContains:
			value = ".*"
		case domain.ConditionFromDomain, domain.ConditionFromExact, domain.ConditionToDomain,
			domain.ConditionAttachmentType, domain.ConditionFolderIs, domain.ConditionFolderIsNot:
			value = "x"
		}
		_ = Evaluate(leaf(ct, value), Candidate{})
	}
}

func TestAddrDomainEquals(t *testing.T) {
	if !addrDomainEquals("a@Example.COM", "example.com") {
		t.Error("addrDomainEquals should be case-insensitive")
	}
	if addrDomainEquals("not-an-email", "example.com") {
		t.Error("addrDomainEquals should reject an address with no @")
	}
}

func TestRegexMatches_InvalidPatternReturnsFalseNotPanic(t *testing.T) {
	if regexMatches("(", "anything") {
		t.Error("regexMatches with an invalid pattern should return false")
	}
}
