package repo

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/yurydemin/marchi/internal/db/writer"
	"github.com/yurydemin/marchi/internal/domain"
)

// SyncLogsRepo is the sync_logs table's repository.
type SyncLogsRepo struct {
	db *sql.DB
	w  writer.Writer
}

func NewSyncLogsRepo(db *sql.DB, w writer.Writer) *SyncLogsRepo {
	return &SyncLogsRepo{db: db, w: w}
}

// Start records the beginning of a sync run for accountID and returns its ID.
func (r *SyncLogsRepo) Start(ctx context.Context, accountID int64) (int64, error) {
	var id int64
	err := r.w.Do(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO sync_logs (account_id, started_at, status)
			VALUES (?, ?, 'running')`,
			accountID, formatSQLiteTime(time.Now()),
		)
		if err != nil {
			return fmt.Errorf("repo: starting sync log for account %d: %w", accountID, err)
		}
		id, err = res.LastInsertId()
		return err
	})
	return id, err
}

// Finish records the end of a sync run. Only s's EmailsProcessed,
// EmailsArchived, EmailsSkipped, BytesDownloaded, Errors, Status, and
// ErrorMsg fields are used — ID/AccountID/StartedAt/EndedAt are Start's and
// Finish's own responsibility, not the caller's.
func (r *SyncLogsRepo) Finish(ctx context.Context, id int64, s *domain.SyncLog) error {
	return r.w.Do(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE sync_logs SET
				ended_at = ?, emails_processed = ?, emails_archived = ?,
				emails_skipped = ?, bytes_downloaded = ?, errors = ?,
				status = ?, error_msg = ?
			WHERE id = ?`,
			formatSQLiteTime(time.Now()), s.EmailsProcessed, s.EmailsArchived,
			s.EmailsSkipped, s.BytesDownloaded, s.Errors,
			string(s.Status), nullIfEmpty(s.ErrorMsg), id,
		)
		if err != nil {
			return fmt.Errorf("repo: finishing sync log %d: %w", id, err)
		}
		return nil
	})
}

// ListByAccount returns accountID's most recent sync runs, newest first.
func (r *SyncLogsRepo) ListByAccount(ctx context.Context, accountID int64, limit int) ([]*domain.SyncLog, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, account_id, started_at, ended_at, emails_processed, emails_archived,
		       emails_skipped, bytes_downloaded, errors, status, error_msg
		FROM sync_logs WHERE account_id = ? ORDER BY id DESC LIMIT ?`, accountID, limit)
	if err != nil {
		return nil, fmt.Errorf("repo: listing sync logs for account %d: %w", accountID, err)
	}
	defer rows.Close()
	return scanSyncLogs(rows)
}

// ListRecent returns the most recent sync runs across every account, newest first.
func (r *SyncLogsRepo) ListRecent(ctx context.Context, limit int) ([]*domain.SyncLog, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, account_id, started_at, ended_at, emails_processed, emails_archived,
		       emails_skipped, bytes_downloaded, errors, status, error_msg
		FROM sync_logs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("repo: listing recent sync logs: %w", err)
	}
	defer rows.Close()
	return scanSyncLogs(rows)
}

// ListRecentPage is ListRecent with offset/limit pagination, for
// GET /api/v1/logs/sync (FR-API-02: "журналы синхронизации, пагинация").
// A separate method rather than adding offset to ListRecent so the CLI's
// existing `status` command (which only ever wants "the last N") doesn't
// need to change.
func (r *SyncLogsRepo) ListRecentPage(ctx context.Context, offset, limit int) ([]*domain.SyncLog, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, account_id, started_at, ended_at, emails_processed, emails_archived,
		       emails_skipped, bytes_downloaded, errors, status, error_msg
		FROM sync_logs ORDER BY id DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("repo: listing sync logs page: %w", err)
	}
	defer rows.Close()
	return scanSyncLogs(rows)
}

// CountAll returns the total number of sync_logs rows, for ListRecentPage's
// pagination metadata.
func (r *SyncLogsRepo) CountAll(ctx context.Context) (int, error) {
	var n int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sync_logs`).Scan(&n); err != nil {
		return 0, fmt.Errorf("repo: counting sync logs: %w", err)
	}
	return n, nil
}

func scanSyncLogs(rows *sql.Rows) ([]*domain.SyncLog, error) {
	var logs []*domain.SyncLog
	for rows.Next() {
		var (
			l                  domain.SyncLog
			startedAt, endedAt sql.NullString
			status             string
			errMsg             sql.NullString
		)
		err := rows.Scan(
			&l.ID, &l.AccountID, &startedAt, &endedAt, &l.EmailsProcessed, &l.EmailsArchived,
			&l.EmailsSkipped, &l.BytesDownloaded, &l.Errors, &status, &errMsg,
		)
		if err != nil {
			return nil, fmt.Errorf("repo: scanning sync log: %w", err)
		}
		l.Status = domain.SyncLogStatus(status)
		l.ErrorMsg = errMsg.String

		if startedAt.Valid {
			if l.StartedAt, err = parseSQLiteTime(startedAt.String); err != nil {
				return nil, fmt.Errorf("repo: parsing started_at: %w", err)
			}
		}
		if endedAt.Valid {
			if l.EndedAt, err = parseSQLiteTime(endedAt.String); err != nil {
				return nil, fmt.Errorf("repo: parsing ended_at: %w", err)
			}
		}
		logs = append(logs, &l)
	}
	return logs, rows.Err()
}
