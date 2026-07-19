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
		SELECT id, name, priority, conditions_json, action, is_active, created_at
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
		SELECT id, name, priority, conditions_json, action, is_active, created_at
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
		SELECT id, name, priority, conditions_json, action, is_active, created_at
		FROM rules WHERE id = ?`, id)
	return scanRule(row)
}

func scanRule(row rowScanner) (*domain.Rule, error) {
	var (
		rule                   domain.Rule
		conditionsJSON, action string
		isActive               int
		createdAt              string
	)
	err := row.Scan(
		&rule.ID, &rule.Name, &rule.Priority, &conditionsJSON, &action,
		&isActive, &createdAt,
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

	return &rule, nil
}
