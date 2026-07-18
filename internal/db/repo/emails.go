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

// ListAll returns every email in the archive, ordered by id — used for a
// full reindex (FR-SR-04), which needs every locally-archived .eml
// regardless of which account or folder it belongs to.
func (r *EmailsRepo) ListAll(ctx context.Context) ([]*domain.Email, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, message_id, account_id, folder_id, uid, subject, from_addr,
		       to_addrs, cc_addrs, date, size, has_attachments, flags,
		       storage_location, local_path, s3_key, s3_etag, s3_sha256,
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
		       storage_location, local_path, s3_key, s3_etag, s3_sha256,
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
		       storage_location, local_path, s3_key, s3_etag, s3_sha256,
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

func scanEmail(rows *sql.Rows) (*domain.Email, error) {
	var (
		e                                 domain.Email
		fromAddr, s3Key, s3ETag, s3SHA256 sql.NullString
		toJSON, ccJSON, flagsJSON         string
		date                              sql.NullString
		hasAttachments                    int
		createdAt, updatedAt              string
	)
	err := rows.Scan(
		&e.ID, &e.MessageID, &e.AccountID, &e.FolderID, &e.UID, &e.Subject, &fromAddr,
		&toJSON, &ccJSON, &date, &e.Size, &hasAttachments, &flagsJSON,
		&e.StorageLocation, &e.LocalPath, &s3Key, &s3ETag, &s3SHA256,
		&createdAt, &updatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("repo: scanning email: %w", err)
	}

	e.FromAddr = fromAddr.String
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
