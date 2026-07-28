package repo

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/yurydemin/marchi/internal/db/writer"
	"github.com/yurydemin/marchi/internal/domain"
)

// AuditLogRepo is the audit_log table's repository.
type AuditLogRepo struct {
	db *sql.DB
	w  writer.Writer
}

func NewAuditLogRepo(db *sql.DB, w writer.Writer) *AuditLogRepo {
	return &AuditLogRepo{db: db, w: w}
}

// Insert records one audit event. created_at is stamped by the schema's
// own DEFAULT CURRENT_TIMESTAMP, not supplied by the caller, so every
// entry's timestamp reflects when it actually landed in the database
// rather than whatever clock the calling goroutine happened to read.
func (r *AuditLogRepo) Insert(ctx context.Context, eventType domain.AuditEventType, ip, summary string) error {
	return r.w.Do(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO audit_log (event_type, ip, summary) VALUES (?, ?, ?)`,
			string(eventType), nullIfEmpty(ip), summary,
		)
		if err != nil {
			return fmt.Errorf("repo: inserting audit log entry: %w", err)
		}
		return nil
	})
}

// List returns the most recent limit audit log entries, newest first —
// the Settings UI's log viewer has no need for arbitrary paging, just
// "what happened recently".
func (r *AuditLogRepo) List(ctx context.Context, limit int) ([]*domain.AuditLogEntry, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, created_at, event_type, ip, summary
		FROM audit_log ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("repo: listing audit log: %w", err)
	}
	defer rows.Close()

	var entries []*domain.AuditLogEntry
	for rows.Next() {
		var (
			e         domain.AuditLogEntry
			eventType string
			ip        sql.NullString
			createdAt string
		)
		if err := rows.Scan(&e.ID, &createdAt, &eventType, &ip, &e.Summary); err != nil {
			return nil, fmt.Errorf("repo: scanning audit log entry: %w", err)
		}
		e.EventType = domain.AuditEventType(eventType)
		e.IP = ip.String
		t, err := parseSQLiteTime(createdAt)
		if err != nil {
			return nil, fmt.Errorf("repo: parsing audit log created_at: %w", err)
		}
		e.CreatedAt = t
		entries = append(entries, &e)
	}
	return entries, rows.Err()
}
