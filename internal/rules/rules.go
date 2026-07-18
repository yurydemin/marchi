// Package rules evaluates archive rules (FR-RE-01..03) against a message
// fetched but not yet archived. The condition tree's data shape
// (domain.RuleNode) lives in internal/domain — this package is the
// evaluation algorithm on top of it, plus the sync-engine-facing Candidate
// type and validation the tree must pass before it's ever evaluated.
package rules

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/yurydemin/marchi/internal/domain"
)

// MaxDepth is FR-RE-02's "вложенность до 3 уровней" — a top-level group
// counts as depth 1, so at most two levels of nested groups are allowed
// beneath it.
const MaxDepth = 3

// Candidate is what a rule's condition tree is evaluated against: the
// properties of a message internal/sync's archiveOne has already
// extracted via mimeparse, before writing anything to storage (FR-RE-01).
// From/To/Cc are the full RFC 5322 header form (for *_contains regex
// matches); FromAddr/ToAddrs/CcAddrs are the bare addresses (for
// *_exact/*_domain matches) — mirrors mimeparse.Metadata's own split for
// the same reason.
type Candidate struct {
	From            string
	FromAddr        string
	To              []string
	ToAddrs         []string
	Cc              []string
	CcAddrs         []string
	Subject         string
	HasAttachments  bool
	AttachmentTypes []string
	Size            int64
	Date            time.Time
	FolderName      string
	AccountID       int64
}

// Validate checks that n is structurally sound before it's ever stored or
// evaluated: every group has a known Op and at least one child, every
// leaf has a known ConditionType and a Value that condition type can
// actually parse (a regex that compiles, a valid int, etc.), and the tree
// doesn't exceed MaxDepth. Called by the Rules REST API and the YAML
// loader (step 3) on every rule before it's persisted — a rule that fails
// Validate never reaches the DB, so FirstMatch never has to handle a
// malformed tree at evaluation time.
func Validate(n domain.RuleNode) error {
	return validateDepth(n, 1)
}

func validateDepth(n domain.RuleNode, depth int) error {
	isGroup := n.Op != ""
	isLeaf := n.Type != ""
	switch {
	case isGroup == isLeaf:
		return fmt.Errorf("rules: node must set exactly one of op or type, got op=%q type=%q", n.Op, n.Type)
	case isGroup:
		if n.Op != domain.OpAnd && n.Op != domain.OpOr {
			return fmt.Errorf("rules: unknown op %q", n.Op)
		}
		if len(n.Children) == 0 {
			return fmt.Errorf("rules: group node (op=%q) has no children", n.Op)
		}
		if depth > MaxDepth {
			return fmt.Errorf("rules: condition tree exceeds max depth %d", MaxDepth)
		}
		for _, child := range n.Children {
			if err := validateDepth(child, depth+1); err != nil {
				return err
			}
		}
		return nil
	default: // leaf
		return validateLeafValue(n.Type, n.Value)
	}
}

func validateLeafValue(t domain.ConditionType, value string) error {
	switch t {
	case domain.ConditionFromContains, domain.ConditionToContains, domain.ConditionSubjectContains:
		if _, err := regexp.Compile(value); err != nil {
			return fmt.Errorf("rules: %s: invalid regex %q: %w", t, value, err)
		}
	case domain.ConditionFromDomain, domain.ConditionFromExact, domain.ConditionToDomain,
		domain.ConditionAttachmentType, domain.ConditionFolderIs, domain.ConditionFolderIsNot:
		if value == "" {
			return fmt.Errorf("rules: %s: value must not be empty", t)
		}
	case domain.ConditionHasAttachments:
		if _, err := strconv.ParseBool(value); err != nil {
			return fmt.Errorf("rules: %s: invalid boolean %q: %w", t, value, err)
		}
	case domain.ConditionSizeGreaterThan, domain.ConditionSizeLessThan:
		if _, err := strconv.ParseInt(value, 10, 64); err != nil {
			return fmt.Errorf("rules: %s: invalid byte count %q: %w", t, value, err)
		}
	case domain.ConditionDateAfter, domain.ConditionDateBefore:
		if _, err := time.Parse(time.RFC3339, value); err != nil {
			return fmt.Errorf("rules: %s: invalid ISO 8601 date %q: %w", t, value, err)
		}
	case domain.ConditionAccountIs:
		if _, err := strconv.ParseInt(value, 10, 64); err != nil {
			return fmt.Errorf("rules: %s: invalid account id %q: %w", t, value, err)
		}
	default:
		return fmt.Errorf("rules: unknown condition type %q", t)
	}
	return nil
}

// Evaluate reports whether n matches c. Assumes n has already passed
// Validate — a leaf whose Value can't be parsed here (e.g. a regex that
// somehow reached this point uncompiled) reports false rather than
// panicking, since Evaluate itself never returns an error.
func Evaluate(n domain.RuleNode, c Candidate) bool {
	if n.Op != "" {
		switch n.Op {
		case domain.OpAnd:
			for _, child := range n.Children {
				if !Evaluate(child, c) {
					return false
				}
			}
			return true
		case domain.OpOr:
			for _, child := range n.Children {
				if Evaluate(child, c) {
					return true
				}
			}
			return false
		default:
			return false
		}
	}
	return evaluateLeaf(n.Type, n.Value, c)
}

func evaluateLeaf(t domain.ConditionType, value string, c Candidate) bool {
	switch t {
	case domain.ConditionFromContains:
		return regexMatches(value, c.From)
	case domain.ConditionFromDomain:
		return addrDomainEquals(c.FromAddr, value)
	case domain.ConditionFromExact:
		return strings.EqualFold(c.FromAddr, value)
	case domain.ConditionToContains:
		return anyRegexMatches(value, c.To)
	case domain.ConditionToDomain:
		return anyAddrDomainEquals(c.ToAddrs, value)
	case domain.ConditionSubjectContains:
		return regexMatches(value, c.Subject)
	case domain.ConditionHasAttachments:
		want, err := strconv.ParseBool(value)
		return err == nil && c.HasAttachments == want
	case domain.ConditionAttachmentType:
		for _, mt := range c.AttachmentTypes {
			if strings.EqualFold(mt, value) {
				return true
			}
		}
		return false
	case domain.ConditionSizeGreaterThan:
		n, err := strconv.ParseInt(value, 10, 64)
		return err == nil && c.Size > n
	case domain.ConditionSizeLessThan:
		n, err := strconv.ParseInt(value, 10, 64)
		return err == nil && c.Size < n
	case domain.ConditionDateAfter:
		d, err := time.Parse(time.RFC3339, value)
		return err == nil && c.Date.After(d)
	case domain.ConditionDateBefore:
		d, err := time.Parse(time.RFC3339, value)
		return err == nil && c.Date.Before(d)
	case domain.ConditionFolderIs:
		return strings.EqualFold(c.FolderName, value)
	case domain.ConditionFolderIsNot:
		return !strings.EqualFold(c.FolderName, value)
	case domain.ConditionAccountIs:
		n, err := strconv.ParseInt(value, 10, 64)
		return err == nil && c.AccountID == n
	default:
		return false
	}
}

func regexMatches(pattern, s string) bool {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return false
	}
	return re.MatchString(s)
}

func anyRegexMatches(pattern string, ss []string) bool {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return false
	}
	for _, s := range ss {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

func addrDomainEquals(addr, domainWant string) bool {
	_, d, ok := strings.Cut(addr, "@")
	return ok && strings.EqualFold(d, domainWant)
}

func anyAddrDomainEquals(addrs []string, domainWant string) bool {
	for _, a := range addrs {
		if addrDomainEquals(a, domainWant) {
			return true
		}
	}
	return false
}

// FirstMatch returns the first rule in rules (expected pre-sorted by
// Priority ascending — see repo.RulesRepo.ListActive) whose condition
// tree matches c, or nil if none do. A nil result means the sync
// engine's default applies: archive everything (backward compatible with
// Phase 1/2, where no rule engine existed at all).
func FirstMatch(rules []*domain.Rule, c Candidate) *domain.Rule {
	for _, r := range rules {
		if Evaluate(r.Conditions, c) {
			return r
		}
	}
	return nil
}
