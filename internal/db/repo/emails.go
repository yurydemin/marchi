package repo

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/yurydemin/marchi/internal/db/writer"
	"github.com/yurydemin/marchi/internal/domain"
)

const sqliteTimeFormat = "2006-01-02 15:04:05"

// EmailsRepo is the emails table's repository.
type EmailsRepo struct {
	db *sql.DB
	w  writer.Writer
}

func NewEmailsRepo(db *sql.DB, w writer.Writer) *EmailsRepo {
	return &EmailsRepo{db: db, w: w}
}

// Insert adds e within an existing transaction — the Sync Engine combines
// this with FoldersRepo.UpdateLastUID into one atomic Single-Writer write,
// which is why this takes a *sql.Tx directly instead of wrapping its own
// writer.Do call. Returns e's assigned ID.
func (r *EmailsRepo) Insert(ctx context.Context, tx *sql.Tx, e *domain.Email) (int64, error) {
	toJSON, err := marshalStrings(e.ToAddrs)
	if err != nil {
		return 0, err
	}
	ccJSON, err := marshalStrings(e.CcAddrs)
	if err != nil {
		return 0, err
	}
	flagsJSON, err := marshalStrings(e.Flags)
	if err != nil {
		return 0, err
	}

	res, err := tx.ExecContext(ctx, `
		INSERT INTO emails (
			message_id, account_id, folder_id, uid, subject, from_addr,
			to_addrs, cc_addrs, date, size, has_attachments, flags,
			storage_location, local_path
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.MessageID, e.AccountID, e.FolderID, e.UID, e.Subject, nullIfEmpty(e.FromAddr),
		toJSON, ccJSON, formatSQLiteTime(e.Date), e.Size, boolToInt(e.HasAttachments), flagsJSON,
		e.StorageLocation, e.LocalPath,
	)
	if err != nil {
		return 0, fmt.Errorf("repo: inserting email (account=%d folder=%d uid=%d): %w", e.AccountID, e.FolderID, e.UID, err)
	}
	return res.LastInsertId()
}

// Stats is the archive-wide summary the Dashboard (FR-WU-02) and
// GET /api/v1/stats need: total email count, and storage volume broken
// down by where it currently lives.
type Stats struct {
	Total           int
	LocalBytes      int64
	S3Bytes         int64
	EmailsByAccount map[int64]int
}

// Stats computes the archive-wide summary in two queries: one aggregate
// (total count + size by storage_location) and one GROUP BY (count per
// account) — cheap enough to run on every dashboard load without needing
// to cache it anywhere yet.
func (r *EmailsRepo) Stats(ctx context.Context) (Stats, error) {
	var s Stats
	row := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*),
		       COALESCE(SUM(CASE WHEN storage_location = 'local' THEN size ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN storage_location = 's3' THEN size ELSE 0 END), 0)
		FROM emails`)
	if err := row.Scan(&s.Total, &s.LocalBytes, &s.S3Bytes); err != nil {
		return Stats{}, fmt.Errorf("repo: computing email stats: %w", err)
	}

	rows, err := r.db.QueryContext(ctx, `SELECT account_id, COUNT(*) FROM emails GROUP BY account_id`)
	if err != nil {
		return Stats{}, fmt.Errorf("repo: computing per-account email counts: %w", err)
	}
	defer rows.Close()

	s.EmailsByAccount = make(map[int64]int)
	for rows.Next() {
		var accountID int64
		var count int
		if err := rows.Scan(&accountID, &count); err != nil {
			return Stats{}, fmt.Errorf("repo: scanning per-account email count: %w", err)
		}
		s.EmailsByAccount[accountID] = count
	}
	return s, rows.Err()
}

// GetByID returns the email with the given id, or sql.ErrNoRows.
func (r *EmailsRepo) GetByID(ctx context.Context, id int64) (*domain.Email, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, message_id, account_id, folder_id, uid, subject, from_addr,
		       to_addrs, cc_addrs, date, size, has_attachments, flags,
		       storage_location, local_path, s3_key, s3_etag, s3_sha256, s3_only_since,
		       created_at, updated_at
		FROM emails WHERE id = ?`, id)
	return scanEmail(row)
}

// ListAll returns every email in the archive, ordered by id — used for a
// full reindex (FR-SR-04), which needs every locally-archived .eml
// regardless of which account or folder it belongs to.
func (r *EmailsRepo) ListAll(ctx context.Context) ([]*domain.Email, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, message_id, account_id, folder_id, uid, subject, from_addr,
		       to_addrs, cc_addrs, date, size, has_attachments, flags,
		       storage_location, local_path, s3_key, s3_etag, s3_sha256, s3_only_since,
		       created_at, updated_at
		FROM emails ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("repo: listing all emails: %w", err)
	}
	defer rows.Close()

	var emails []*domain.Email
	for rows.Next() {
		e, err := scanEmail(rows)
		if err != nil {
			return nil, err
		}
		emails = append(emails, e)
	}
	return emails, rows.Err()
}

// ListByAccount returns every email archived for accountID, across every
// folder — used to enumerate what needs removing from the search index
// before an account (and its cascade-deleted rows) is deleted (FR-AM-06).
func (r *EmailsRepo) ListByAccount(ctx context.Context, accountID int64) ([]*domain.Email, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, message_id, account_id, folder_id, uid, subject, from_addr,
		       to_addrs, cc_addrs, date, size, has_attachments, flags,
		       storage_location, local_path, s3_key, s3_etag, s3_sha256, s3_only_since,
		       created_at, updated_at
		FROM emails WHERE account_id = ? ORDER BY id`, accountID)
	if err != nil {
		return nil, fmt.Errorf("repo: listing emails for account %d: %w", accountID, err)
	}
	defer rows.Close()

	var emails []*domain.Email
	for rows.Next() {
		e, err := scanEmail(rows)
		if err != nil {
			return nil, err
		}
		emails = append(emails, e)
	}
	return emails, rows.Err()
}

// ListByFolder returns every email recorded for folderID, ordered by uid.
func (r *EmailsRepo) ListByFolder(ctx context.Context, folderID int64) ([]*domain.Email, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, message_id, account_id, folder_id, uid, subject, from_addr,
		       to_addrs, cc_addrs, date, size, has_attachments, flags,
		       storage_location, local_path, s3_key, s3_etag, s3_sha256, s3_only_since,
		       created_at, updated_at
		FROM emails WHERE folder_id = ? ORDER BY uid`, folderID)
	if err != nil {
		return nil, fmt.Errorf("repo: listing emails for folder %d: %w", folderID, err)
	}
	defer rows.Close()

	var emails []*domain.Email
	for rows.Next() {
		e, err := scanEmail(rows)
		if err != nil {
			return nil, err
		}
		emails = append(emails, e)
	}
	return emails, rows.Err()
}

// ListLocalDueForS3Eviction returns accountID's Stage A emails eligible
// to move into Stage B (local copy evicted, S3-only): still stored
// locally, archived at or before cutoff, and — critically — with a
// confirmed S3 upload (s3_etag IS NOT NULL). That last condition is
// brief.md §4.6's safety invariant: never evict the only copy of an email
// before its S3 mirror is confirmed to exist, no matter how long
// retention_move_to_s3_days says it's been.
func (r *EmailsRepo) ListLocalDueForS3Eviction(ctx context.Context, accountID int64, cutoff time.Time) ([]*domain.Email, error) {
	return r.queryEmails(ctx, `
		SELECT id, message_id, account_id, folder_id, uid, subject, from_addr,
		       to_addrs, cc_addrs, date, size, has_attachments, flags,
		       storage_location, local_path, s3_key, s3_etag, s3_sha256, s3_only_since,
		       created_at, updated_at
		FROM emails
		WHERE account_id = ? AND storage_location = 'local' AND s3_etag IS NOT NULL AND created_at <= ?
		ORDER BY id`, accountID, formatSQLiteTime(cutoff))
}

// ListLocalDueForDirectDelete returns accountID's Stage A emails eligible
// for outright deletion (no S3 backup to fall back to) — the path
// retention takes when S3 mirroring isn't enabled at all, using
// retention_local_days as the trigger instead of retention_move_to_s3_days.
func (r *EmailsRepo) ListLocalDueForDirectDelete(ctx context.Context, accountID int64, cutoff time.Time) ([]*domain.Email, error) {
	return r.queryEmails(ctx, `
		SELECT id, message_id, account_id, folder_id, uid, subject, from_addr,
		       to_addrs, cc_addrs, date, size, has_attachments, flags,
		       storage_location, local_path, s3_key, s3_etag, s3_sha256, s3_only_since,
		       created_at, updated_at
		FROM emails
		WHERE account_id = ? AND storage_location = 'local' AND created_at <= ?
		ORDER BY id`, accountID, formatSQLiteTime(cutoff))
}

// ListS3OnlyDueForDeletion returns accountID's Stage B emails eligible
// for Stage C (deleted entirely from S3 and SQLite) — S3-only, and it's
// been at least retention_s3_days since s3_only_since (Stage B started).
func (r *EmailsRepo) ListS3OnlyDueForDeletion(ctx context.Context, accountID int64, cutoff time.Time) ([]*domain.Email, error) {
	return r.queryEmails(ctx, `
		SELECT id, message_id, account_id, folder_id, uid, subject, from_addr,
		       to_addrs, cc_addrs, date, size, has_attachments, flags,
		       storage_location, local_path, s3_key, s3_etag, s3_sha256, s3_only_since,
		       created_at, updated_at
		FROM emails
		WHERE account_id = ? AND storage_location = 's3' AND s3_only_since IS NOT NULL AND s3_only_since <= ?
		ORDER BY id`, accountID, formatSQLiteTime(cutoff))
}

func (r *EmailsRepo) queryEmails(ctx context.Context, query string, args ...any) ([]*domain.Email, error) {
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("repo: querying emails: %w", err)
	}
	defer rows.Close()

	var emails []*domain.Email
	for rows.Next() {
		e, err := scanEmail(rows)
		if err != nil {
			return nil, err
		}
		emails = append(emails, e)
	}
	return emails, rows.Err()
}

// MarkMovedToS3Only transitions an email from Stage A to Stage B: the
// local copy is gone (LocalPath cleared), storage_location becomes 's3',
// and s3_only_since is stamped to now so Stage B's own retention_s3_days
// threshold has a starting point. now is a parameter (not SQLite's own
// CURRENT_TIMESTAMP) so it tracks internal/retention.Runner's injectable
// clock (Deps.Now) rather than the real wall clock — a test driving a
// simulated "now" needs Stage B's start time to move with it, not with
// whatever moment the test actually happened to run. Deleting the actual
// local file is the caller's responsibility (internal/retention.Runner)
// — this only updates bookkeeping, inside the caller's own Single-Writer
// transaction.
func (r *EmailsRepo) MarkMovedToS3Only(ctx context.Context, tx *sql.Tx, emailID int64, now time.Time) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE emails SET storage_location = 's3', local_path = NULL,
		       s3_only_since = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ?`, now, emailID)
	if err != nil {
		return fmt.Errorf("repo: marking email %d moved to s3-only: %w", emailID, err)
	}
	return nil
}

// DeleteCompletely removes emailID's row (cascading to its attachments
// via ON DELETE CASCADE). Deleting the local file, the S3 object, and the
// search index entry are all the caller's responsibility — this is purely
// the SQLite side, inside the caller's own Single-Writer transaction.
func (r *EmailsRepo) DeleteCompletely(ctx context.Context, tx *sql.Tx, emailID int64) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM emails WHERE id = ?`, emailID); err != nil {
		return fmt.Errorf("repo: deleting email %d: %w", emailID, err)
	}
	return nil
}

func scanEmail(rows rowScanner) (*domain.Email, error) {
	var (
		e                                            domain.Email
		fromAddr, localPath, s3Key, s3ETag, s3SHA256 sql.NullString
		toJSON, ccJSON, flagsJSON                    string
		date, s3OnlySince                            sql.NullString
		hasAttachments                               int
		createdAt, updatedAt                         string
	)
	err := rows.Scan(
		&e.ID, &e.MessageID, &e.AccountID, &e.FolderID, &e.UID, &e.Subject, &fromAddr,
		&toJSON, &ccJSON, &date, &e.Size, &hasAttachments, &flagsJSON,
		&e.StorageLocation, &localPath, &s3Key, &s3ETag, &s3SHA256, &s3OnlySince,
		&createdAt, &updatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("repo: scanning email: %w", err)
	}

	e.FromAddr = fromAddr.String
	e.LocalPath = localPath.String
	e.S3Key = s3Key.String
	e.S3ETag = s3ETag.String
	e.S3SHA256 = s3SHA256.String
	e.HasAttachments = hasAttachments != 0

	if e.ToAddrs, err = unmarshalStrings(toJSON); err != nil {
		return nil, err
	}
	if e.CcAddrs, err = unmarshalStrings(ccJSON); err != nil {
		return nil, err
	}
	if e.Flags, err = unmarshalStrings(flagsJSON); err != nil {
		return nil, err
	}

	if date.Valid {
		if e.Date, err = parseSQLiteTime(date.String); err != nil {
			return nil, fmt.Errorf("repo: parsing email date: %w", err)
		}
	}
	if s3OnlySince.Valid {
		if e.S3OnlySince, err = parseSQLiteTime(s3OnlySince.String); err != nil {
			return nil, fmt.Errorf("repo: parsing s3_only_since: %w", err)
		}
	}
	if e.CreatedAt, err = parseSQLiteTime(createdAt); err != nil {
		return nil, fmt.Errorf("repo: parsing created_at: %w", err)
	}
	if e.UpdatedAt, err = parseSQLiteTime(updatedAt); err != nil {
		return nil, fmt.Errorf("repo: parsing updated_at: %w", err)
	}

	return &e, nil
}

func marshalStrings(v []string) (string, error) {
	if v == nil {
		v = []string{}
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("repo: marshaling string list: %w", err)
	}
	return string(b), nil
}

func unmarshalStrings(s string) ([]string, error) {
	if s == "" {
		return nil, nil
	}
	var v []string
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return nil, fmt.Errorf("repo: unmarshaling string list %q: %w", s, err)
	}
	return v, nil
}

// formatSQLiteTime returns nil (SQL NULL) for a zero time.Time — an
// email with no parseable Date header — rather than storing the year-1
// zero value, which would sort before every real date and read as
// meaningless noise.
func formatSQLiteTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(sqliteTimeFormat)
}
