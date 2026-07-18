package rules

import (
	"context"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/yurydemin/marchi/internal/db/repo"
	"github.com/yurydemin/marchi/internal/domain"
)

// yamlFile is rules.yaml's top-level shape (FR-RE-05):
//
//	rules:
//	  - name: "skip newsletters"
//	    priority: 0
//	    action: skip
//	    conditions:
//	      type: subject_contains
//	      value: "(?i)newsletter"
type yamlFile struct {
	Rules []domain.Rule `yaml:"rules"`
}

// ParseYAML parses and validates data as a rules.yaml file. Every rule's
// Action and Conditions tree is checked the same way the Rules REST API
// checks a submitted rule (ParseRuleAction, Validate) — a file with one
// bad rule fails entirely rather than silently loading the rest, since a
// partially-applied config file is a worse failure mode than a config
// file that's simply rejected with a clear error.
func ParseYAML(data []byte) ([]domain.Rule, error) {
	var f yamlFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("rules: parsing YAML: %w", err)
	}
	for i := range f.Rules {
		r := &f.Rules[i]
		if r.Name == "" {
			return nil, fmt.Errorf("rules: rule at index %d has no name", i)
		}
		if _, err := domain.ParseRuleAction(string(r.Action)); err != nil {
			return nil, fmt.Errorf("rules: rule %q: %w", r.Name, err)
		}
		if err := Validate(r.Conditions); err != nil {
			return nil, fmt.Errorf("rules: rule %q: %w", r.Name, err)
		}
	}
	return f.Rules, nil
}

// SyncYAML applies parsed YAML rules to rulesRepo, matching existing rows
// by Name (a hand-authored file has no database IDs to key off of).
// Rules present in the file are created or updated in place; rules
// already in the database but absent from the file are left alone.
//
// This is a deliberately one-directional, non-destructive sync — rules.yaml
// is one of two ways to manage rules (FR-RE-05 also names the Web UI), and
// a rule created or edited through the UI/REST API must not vanish just
// because it happens not to be listed in a YAML file someone is also
// using, possibly for an unrelated subset of rules. Removing a rule is a
// UI/REST-only operation (mirrors FR-AM-06's account deletion needing
// explicit confirmation — deletion is never a side effect of another
// action here).
func SyncYAML(ctx context.Context, rulesRepo *repo.RulesRepo, yamlRules []domain.Rule) error {
	existing, err := rulesRepo.List(ctx)
	if err != nil {
		return fmt.Errorf("rules: listing existing rules: %w", err)
	}
	byName := make(map[string]*domain.Rule, len(existing))
	for _, r := range existing {
		byName[r.Name] = r
	}

	for _, r := range yamlRules {
		r := r
		if current, ok := byName[r.Name]; ok {
			r.ID = current.ID
			if err := rulesRepo.Update(ctx, &r); err != nil {
				return fmt.Errorf("rules: updating rule %q: %w", r.Name, err)
			}
			continue
		}
		if _, err := rulesRepo.Create(ctx, &r); err != nil {
			return fmt.Errorf("rules: creating rule %q: %w", r.Name, err)
		}
	}
	return nil
}

// LoadYAMLFile reads path, parses it (ParseYAML), and syncs it into
// rulesRepo (SyncYAML) in one call — what both the startup load and every
// fsnotify-triggered reload in WatchYAML actually do. A missing file is
// not an error (FR-RE-05: rules.yaml is optional); it's reported via the
// bool return so callers can distinguish "nothing to do" from "loaded
// successfully".
func LoadYAMLFile(ctx context.Context, path string, rulesRepo *repo.RulesRepo) (loaded bool, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("rules: reading %s: %w", path, err)
	}
	parsed, err := ParseYAML(data)
	if err != nil {
		return false, err
	}
	if err := SyncYAML(ctx, rulesRepo, parsed); err != nil {
		return false, err
	}
	return true, nil
}
