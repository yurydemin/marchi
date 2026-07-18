package repo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/yurydemin/marchi/internal/db/writer"
	"github.com/yurydemin/marchi/internal/domain"
)

// S3ConfigRepo is the s3_config table's repository — a singleton row
// (FR-S3-02), unlike every other repo in this package.
type S3ConfigRepo struct {
	db *sql.DB
	w  writer.Writer
}

func NewS3ConfigRepo(db *sql.DB, w writer.Writer) *S3ConfigRepo {
	return &S3ConfigRepo{db: db, w: w}
}

// Get returns the singleton row, or sql.ErrNoRows if S3 has never been
// configured (Upsert has never been called) — the zero-config default
// state NFR-RL-01 describes as "работает в локальном режиме".
func (r *S3ConfigRepo) Get(ctx context.Context) (*domain.S3Settings, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT enabled, endpoint, region, bucket,
		       access_key_encrypted, secret_key_encrypted,
		       path_style, storage_class, tls_skip_verify, updated_at
		FROM s3_config WHERE id = 1`)
	return scanS3Settings(row)
}

// Upsert writes the singleton row (id=1), creating it on first use and
// overwriting it entirely thereafter — the Settings UI/API always submits
// the full configuration, so there's no notion of a partial update here
// the way accounts.Update has "" -> keep-existing semantics.
func (r *S3ConfigRepo) Upsert(ctx context.Context, s *domain.S3Settings) error {
	return r.w.Do(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO s3_config (
				id, enabled, endpoint, region, bucket,
				access_key_encrypted, secret_key_encrypted,
				path_style, storage_class, tls_skip_verify, updated_at
			) VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
			ON CONFLICT(id) DO UPDATE SET
				enabled = excluded.enabled,
				endpoint = excluded.endpoint,
				region = excluded.region,
				bucket = excluded.bucket,
				access_key_encrypted = excluded.access_key_encrypted,
				secret_key_encrypted = excluded.secret_key_encrypted,
				path_style = excluded.path_style,
				storage_class = excluded.storage_class,
				tls_skip_verify = excluded.tls_skip_verify,
				updated_at = excluded.updated_at`,
			boolToInt(s.Enabled), s.Endpoint, s.Region, s.Bucket,
			s.AccessKeyEncrypted, s.SecretKeyEncrypted,
			boolToInt(s.PathStyle), s.StorageClass, boolToInt(s.TLSSkipVerify),
		)
		if err != nil {
			return fmt.Errorf("repo: upserting s3_config: %w", err)
		}
		return nil
	})
}

func scanS3Settings(row rowScanner) (*domain.S3Settings, error) {
	var (
		s                              domain.S3Settings
		enabled, pathStyle, tlsSkipVer int
		updatedAt                      string
	)
	err := row.Scan(
		&enabled, &s.Endpoint, &s.Region, &s.Bucket,
		&s.AccessKeyEncrypted, &s.SecretKeyEncrypted,
		&pathStyle, &s.StorageClass, &tlsSkipVer, &updatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		return nil, fmt.Errorf("repo: scanning s3_config: %w", err)
	}
	s.Enabled = enabled != 0
	s.PathStyle = pathStyle != 0
	s.TLSSkipVerify = tlsSkipVer != 0
	s.UpdatedAt, err = parseSQLiteTime(updatedAt)
	if err != nil {
		return nil, fmt.Errorf("repo: parsing updated_at: %w", err)
	}
	return &s, nil
}
