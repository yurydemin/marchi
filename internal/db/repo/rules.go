package repo

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/yurydemin/marchi/internal/db/writer"
	"github.com/yurydemin/marchi/internal/domain"
)

// RulesRepo is the rules table's repository (FR-RE-01/FR-ST-03). Rules
// only govern the archive/skip/mark_read dispatch decision — retention is
// a separate concern (see repo.RetentionSettingsRepo and the accounts
// table's own retention override columns), not a per-rule setting.
type RulesRepo struct {
	db *sql.DB
	w  writer.Writer
}

func NewRulesRepo(db *sql.DB, w writer.Writer) *RulesRepo {
	return &RulesRepo{db: db, w: w}
}

// Create inserts a new rule and returns its assigned ID.
func (r *RulesRepo) Create(ctx context.Context, rule *domain.Rule) (int64, error) {
	conditionsJSON, err := json.Marshal(rule.Conditions)
	if err != nil {
		return 0, fmt.Errorf("repo: marshaling rule conditions: %w", err)
	}

	var id int64
	err = r.w.Do(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO rules (name, priority, conditions_json, action, is_active)
			VALUES (?, ?, ?, ?, ?)`,
			rule.Name, rule.Priority, string(conditionsJSON), string(rule.Action), boolToInt(rule.IsActive),
		)
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	return id, err
}

// Update replaces every mutable column of the rule identified by rule.ID.
func (r *RulesRepo) Update(ctx context.Context, rule *domain.Rule) error {
	conditionsJSON, err := json.Marshal(rule.Conditions)
	if err != nil {
		return fmt.Errorf("repo: marshaling rule conditions: %w", err)
	}

	return r.w.Do(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE rules SET name = ?, priority = ?, conditions_json = ?, action = ?, is_active = ?
			WHERE id = ?`,
			rule.Name, rule.Priority, string(conditionsJSON), string(rule.Action), boolToInt(rule.IsActive), rule.ID,
		)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return sql.ErrNoRows
		}
		return nil
	})
}

// RecordMatches increments match_count and refreshes last_matched_at for
// every rule in counts (rule id -> number of times it matched during one
// sync run) — called once per sync run, not once per message, so
// per-message rule evaluation never turns into a per-message database
// write (see internal/sync's rule-match aggregation in each connector's
// sync engine). Best-effort by convention at the caller: a failure here
// should be logged and swallowed, the same way search-index and
// audit-log writes already are — losing a stats update is never worth
// failing an otherwise-successful sync over.
func (r *RulesRepo) RecordMatches(ctx context.Context, counts map[int64]int) error {
	if len(counts) == 0 {
		return nil
	}
	return r.w.Do(ctx, func(tx *sql.Tx) error {
		for ruleID, n := range counts {
			if n <= 0 {
				continue
			}
			if _, err := tx.ExecContext(ctx, `
				UPDATE rules SET match_count = match_count + ?, last_matched_at = CURRENT_TIMESTAMP
				WHERE id = ?`, n, ruleID); err != nil {
				return fmt.Errorf("repo: recording match for rule %d: %w", ruleID, err)
			}
		}
		return nil
	})
}

// Delete removes the rule identified by id.
func (r *RulesRepo) Delete(ctx context.Context, id int64) error {
	return r.w.Do(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM rules WHERE id = ?`, id)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return sql.ErrNoRows
		}
		return nil
	})
}

// List returns every rule ordered by priority ascending (lower runs
// first — see domain.Rule's doc comment), then id for a stable order
// among equal priorities.
func (r *RulesRepo) List(ctx context.Context) ([]*domain.Rule, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name, priority, conditions_json, action, is_active, created_at, match_count, last_matched_at
		FROM rules ORDER BY priority ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("repo: listing rules: %w", err)
	}
	defer rows.Close()

	var rules []*domain.Rule
	for rows.Next() {
		rule, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		rules = append(rules, rule)
	}
	return rules, rows.Err()
}

// ListActive is List filtered to is_active=1 — what
// internal/rules.FirstMatch actually needs to evaluate against an
// incoming message, so callers on the sync hot path don't fetch and skip
// disabled rules on every message.
func (r *RulesRepo) ListActive(ctx context.Context) ([]*domain.Rule, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name, priority, conditions_json, action, is_active, created_at, match_count, last_matched_at
		FROM rules WHERE is_active = 1 ORDER BY priority ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("repo: listing active rules: %w", err)
	}
	defer rows.Close()

	var rules []*domain.Rule
	for rows.Next() {
		rule, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		rules = append(rules, rule)
	}
	return rules, rows.Err()
}

// GetByID returns the rule with the given id, or sql.ErrNoRows.
func (r *RulesRepo) GetByID(ctx context.Context, id int64) (*domain.Rule, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, name, priority, conditions_json, action, is_active, created_at, match_count, last_matched_at
		FROM rules WHERE id = ?`, id)
	return scanRule(row)
}

func scanRule(row rowScanner) (*domain.Rule, error) {
	var (
		rule                   domain.Rule
		conditionsJSON, action string
		isActive               int
		createdAt              string
		lastMatchedAt          sql.NullString
	)
	err := row.Scan(
		&rule.ID, &rule.Name, &rule.Priority, &conditionsJSON, &action,
		&isActive, &createdAt, &rule.MatchCount, &lastMatchedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		return nil, fmt.Errorf("repo: scanning rule: %w", err)
	}

	if err := json.Unmarshal([]byte(conditionsJSON), &rule.Conditions); err != nil {
		return nil, fmt.Errorf("repo: unmarshaling rule %d conditions: %w", rule.ID, err)
	}
	rule.Action = domain.RuleAction(action)
	rule.IsActive = isActive != 0

	rule.CreatedAt, err = parseSQLiteTime(createdAt)
	if err != nil {
		return nil, fmt.Errorf("repo: parsing created_at: %w", err)
	}
	if lastMatchedAt.Valid {
		if rule.LastMatchedAt, err = parseSQLiteTime(lastMatchedAt.String); err != nil {
			return nil, fmt.Errorf("repo: parsing last_matched_at: %w", err)
		}
	}

	return &rule, nil
}
