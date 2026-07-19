package repo

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/yurydemin/marchi/internal/db/writer"
	"github.com/yurydemin/marchi/internal/domain"
)

// RestoreLogsRepo is the restore_logs table's repository (FR-RS-04).
// Unlike SyncLogsRepo's Start/Finish split (a sync run takes a while and
// needs a "running" row visible mid-flight), a single email's restore is
// a short, mostly-synchronous operation — Create writes the finished
// outcome in one shot.
type RestoreLogsRepo struct {
	db *sql.DB
	w  writer.Writer
}

func NewRestoreLogsRepo(db *sql.DB, w writer.Writer) *RestoreLogsRepo {
	return &RestoreLogsRepo{db: db, w: w}
}

// Create records one restore attempt's outcome and returns its assigned ID.
func (r *RestoreLogsRepo) Create(ctx context.Context, log *domain.RestoreLog) (int64, error) {
	var id int64
	err := r.w.Do(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO restore_logs (email_id, target_account_id, target_folder, method, status, error_msg)
			VALUES (?, ?, ?, ?, ?, ?)`,
			log.EmailID, log.TargetAccountID, log.TargetFolder, log.Method, log.Status, nullIfEmpty(log.ErrorMsg),
		)
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("repo: recording restore log for email %d: %w", log.EmailID, err)
	}
	return id, nil
}

// ListByEmail returns every restore attempt recorded for emailID, newest first.
func (r *RestoreLogsRepo) ListByEmail(ctx context.Context, emailID int64) ([]*domain.RestoreLog, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, email_id, target_account_id, target_folder, method, status, error_msg, created_at
		FROM restore_logs WHERE email_id = ? ORDER BY id DESC`, emailID)
	if err != nil {
		return nil, fmt.Errorf("repo: listing restore logs for email %d: %w", emailID, err)
	}
	defer rows.Close()
	return scanRestoreLogs(rows)
}

// ListRecent returns the most recent limit restore attempts across every
// email, newest first — the archive-wide history view (Phase 3 step 18's
// Restore UI).
func (r *RestoreLogsRepo) ListRecent(ctx context.Context, limit int) ([]*domain.RestoreLog, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, email_id, target_account_id, target_folder, method, status, error_msg, created_at
		FROM restore_logs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("repo: listing recent restore logs: %w", err)
	}
	defer rows.Close()
	return scanRestoreLogs(rows)
}

func scanRestoreLogs(rows *sql.Rows) ([]*domain.RestoreLog, error) {
	var logs []*domain.RestoreLog
	for rows.Next() {
		var (
			log       domain.RestoreLog
			errMsg    sql.NullString
			createdAt string
		)
		if err := rows.Scan(&log.ID, &log.EmailID, &log.TargetAccountID, &log.TargetFolder,
			&log.Method, &log.Status, &errMsg, &createdAt); err != nil {
			return nil, fmt.Errorf("repo: scanning restore log: %w", err)
		}
		log.ErrorMsg = errMsg.String
		var err error
		if log.CreatedAt, err = parseSQLiteTime(createdAt); err != nil {
			return nil, fmt.Errorf("repo: parsing created_at: %w", err)
		}
		logs = append(logs, &log)
	}
	return logs, rows.Err()
}
