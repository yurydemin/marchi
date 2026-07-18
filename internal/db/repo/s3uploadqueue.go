package repo

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/yurydemin/marchi/internal/db/writer"
	"github.com/yurydemin/marchi/internal/domain"
)

// S3UploadQueueRepo is the s3_upload_queue table's repository (FR-S3-06).
type S3UploadQueueRepo struct {
	db *sql.DB
	w  writer.Writer
}

func NewS3UploadQueueRepo(db *sql.DB, w writer.Writer) *S3UploadQueueRepo {
	return &S3UploadQueueRepo{db: db, w: w}
}

// Enqueue inserts a pending upload row for an already-archived email,
// within tx — the Sync Engine calls this from archiveOne's existing
// Single-Writer transaction (alongside the emails insert and last_uid
// update) so an email row and its mirror-upload intent can never diverge:
// if S3 is enabled, both are created atomically or neither is.
func (r *S3UploadQueueRepo) Enqueue(ctx context.Context, tx *sql.Tx, emailID int64, s3Key, localPath string) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO s3_upload_queue (email_id, s3_key, local_path)
		VALUES (?, ?, ?)`,
		emailID, s3Key, localPath)
	if err != nil {
		return fmt.Errorf("repo: enqueuing s3 upload for email %d: %w", emailID, err)
	}
	return nil
}

// ClaimBatch atomically selects up to limit rows eligible to run right now
// (status='pending' and next_attempt_at due) and marks them 'uploading' in
// the same Single-Writer transaction, so two overlapping poll cycles can
// never claim the same row — the upload workers then do their (slow,
// network-bound) work entirely outside of any DB transaction.
func (r *S3UploadQueueRepo) ClaimBatch(ctx context.Context, limit int) ([]*domain.S3UploadQueueItem, error) {
	var items []*domain.S3UploadQueueItem
	err := r.w.Do(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT id, email_id, attachment_id, s3_key, local_path, status,
			       retry_count, error_msg, next_attempt_at, created_at, updated_at
			FROM s3_upload_queue
			WHERE status = ? AND next_attempt_at <= CURRENT_TIMESTAMP
			ORDER BY created_at
			LIMIT ?`, domain.S3QueueStatusPending, limit)
		if err != nil {
			return fmt.Errorf("repo: selecting claimable s3_upload_queue rows: %w", err)
		}
		var claimed []*domain.S3UploadQueueItem
		for rows.Next() {
			item, err := scanS3UploadQueueItem(rows)
			if err != nil {
				rows.Close()
				return err
			}
			claimed = append(claimed, item)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("repo: iterating claimable s3_upload_queue rows: %w", err)
		}
		rows.Close()

		for _, item := range claimed {
			if _, err := tx.ExecContext(ctx, `
				UPDATE s3_upload_queue SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
				domain.S3QueueStatusUploading, item.ID); err != nil {
				return fmt.Errorf("repo: claiming s3_upload_queue row %d: %w", item.ID, err)
			}
			item.Status = domain.S3QueueStatusUploading
		}
		items = claimed
		return nil
	})
	return items, err
}

// MarkDone updates the email's S3 mirror fields and removes the queue row
// in one transaction (FR-S3-06: "обновление s3_etag/s3_sha256, удаление
// записи из очереди").
func (r *S3UploadQueueRepo) MarkDone(ctx context.Context, item *domain.S3UploadQueueItem, etag, sha256Hex string) error {
	return r.w.Do(ctx, func(tx *sql.Tx) error {
		if item.EmailID != 0 {
			if _, err := tx.ExecContext(ctx, `
				UPDATE emails SET s3_key = ?, s3_etag = ?, s3_sha256 = ?, updated_at = CURRENT_TIMESTAMP
				WHERE id = ?`, item.S3Key, etag, sha256Hex, item.EmailID); err != nil {
				return fmt.Errorf("repo: recording s3 upload for email %d: %w", item.EmailID, err)
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM s3_upload_queue WHERE id = ?`, item.ID); err != nil {
			return fmt.Errorf("repo: removing completed s3_upload_queue row %d: %w", item.ID, err)
		}
		return nil
	})
}

// MarkFailed records a failed attempt. If retryCount has reached
// maxAttempts, the row's status becomes permanently 'failed' (no further
// claims); otherwise it goes back to 'pending' with nextAttemptAt as the
// earliest time it can be claimed again (the caller computes the
// exponential backoff delay).
func (r *S3UploadQueueRepo) MarkFailed(ctx context.Context, id int64, retryCount int, maxAttempts int, errMsg string, nextAttemptAt time.Time) error {
	status := domain.S3QueueStatusPending
	if retryCount >= maxAttempts {
		status = domain.S3QueueStatusFailed
	}
	return r.w.Do(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE s3_upload_queue
			SET status = ?, retry_count = ?, error_msg = ?, next_attempt_at = ?, updated_at = CURRENT_TIMESTAMP
			WHERE id = ?`,
			status, retryCount, errMsg, formatSQLiteTime(nextAttemptAt), id)
		if err != nil {
			return fmt.Errorf("repo: recording failed s3 upload attempt for queue row %d: %w", id, err)
		}
		return nil
	})
}

// CountByStatus returns how many queue rows are in each status — the
// Dashboard's S3 queue summary (FR-S3-06/FR-WU-02).
func (r *S3UploadQueueRepo) CountByStatus(ctx context.Context) (map[string]int, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT status, COUNT(*) FROM s3_upload_queue GROUP BY status`)
	if err != nil {
		return nil, fmt.Errorf("repo: counting s3_upload_queue by status: %w", err)
	}
	defer rows.Close()

	counts := make(map[string]int)
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, fmt.Errorf("repo: scanning s3_upload_queue status count: %w", err)
		}
		counts[status] = count
	}
	return counts, rows.Err()
}

func scanS3UploadQueueItem(row rowScanner) (*domain.S3UploadQueueItem, error) {
	var (
		item                              domain.S3UploadQueueItem
		emailID, attachmentID             sql.NullInt64
		errMsg                            sql.NullString
		nextAttemptAt, createdAt, updated string
	)
	err := row.Scan(
		&item.ID, &emailID, &attachmentID, &item.S3Key, &item.LocalPath, &item.Status,
		&item.RetryCount, &errMsg, &nextAttemptAt, &createdAt, &updated,
	)
	if err != nil {
		return nil, fmt.Errorf("repo: scanning s3_upload_queue row: %w", err)
	}
	item.EmailID = emailID.Int64
	item.AttachmentID = attachmentID.Int64
	item.ErrorMsg = errMsg.String

	if item.NextAttemptAt, err = parseSQLiteTime(nextAttemptAt); err != nil {
		return nil, fmt.Errorf("repo: parsing next_attempt_at: %w", err)
	}
	if item.CreatedAt, err = parseSQLiteTime(createdAt); err != nil {
		return nil, fmt.Errorf("repo: parsing created_at: %w", err)
	}
	if item.UpdatedAt, err = parseSQLiteTime(updated); err != nil {
		return nil, fmt.Errorf("repo: parsing updated_at: %w", err)
	}
	return &item, nil
}
