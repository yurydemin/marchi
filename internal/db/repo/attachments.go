package repo

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/yurydemin/marchi/internal/db/writer"
	"github.com/yurydemin/marchi/internal/domain"
)

// AttachmentsRepo is the attachments table's repository.
type AttachmentsRepo struct {
	db *sql.DB
	w  writer.Writer
}

func NewAttachmentsRepo(db *sql.DB, w writer.Writer) *AttachmentsRepo {
	return &AttachmentsRepo{db: db, w: w}
}

// Insert adds a within an existing transaction — the Sync Engine combines
// this with the parent EmailsRepo.Insert and FoldersRepo.UpdateLastUID into
// one atomic Single-Writer transaction, same pattern as EmailsRepo.Insert
// itself.
func (r *AttachmentsRepo) Insert(ctx context.Context, tx *sql.Tx, a *domain.Attachment) (int64, error) {
	res, err := tx.ExecContext(ctx, `
		INSERT INTO attachments (email_id, filename, mime_type, size, content_id, local_path, s3_key)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		a.EmailID, a.Filename, nullIfEmpty(a.MIMEType), a.Size, nullIfEmpty(a.ContentID),
		nullIfEmpty(a.LocalPath), nullIfEmpty(a.S3Key),
	)
	if err != nil {
		return 0, fmt.Errorf("repo: inserting attachment for email %d: %w", a.EmailID, err)
	}
	return res.LastInsertId()
}

// GetByID returns the attachment with the given id, or sql.ErrNoRows.
func (r *AttachmentsRepo) GetByID(ctx context.Context, id int64) (*domain.Attachment, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, email_id, filename, mime_type, size, content_id, local_path, s3_key, created_at
		FROM attachments WHERE id = ?`, id)
	return scanAttachment(row)
}

// ListByEmail returns every attachment recorded for emailID, in insertion order.
func (r *AttachmentsRepo) ListByEmail(ctx context.Context, emailID int64) ([]*domain.Attachment, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, email_id, filename, mime_type, size, content_id, local_path, s3_key, created_at
		FROM attachments WHERE email_id = ? ORDER BY id`, emailID)
	if err != nil {
		return nil, fmt.Errorf("repo: listing attachments for email %d: %w", emailID, err)
	}
	defer rows.Close()

	var attachments []*domain.Attachment
	for rows.Next() {
		a, err := scanAttachment(rows)
		if err != nil {
			return nil, err
		}
		attachments = append(attachments, a)
	}
	return attachments, rows.Err()
}

func scanAttachment(row rowScanner) (*domain.Attachment, error) {
	var (
		a                                     domain.Attachment
		mimeType, contentID, localPath, s3Key sql.NullString
		createdAt                             string
	)
	err := row.Scan(&a.ID, &a.EmailID, &a.Filename, &mimeType, &a.Size, &contentID, &localPath, &s3Key, &createdAt)
	if err != nil {
		return nil, fmt.Errorf("repo: scanning attachment: %w", err)
	}
	a.MIMEType = mimeType.String
	a.ContentID = contentID.String
	a.LocalPath = localPath.String
	a.S3Key = s3Key.String
	if a.CreatedAt, err = parseSQLiteTime(createdAt); err != nil {
		return nil, fmt.Errorf("repo: parsing created_at: %w", err)
	}
	return &a, nil
}
