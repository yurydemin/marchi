package repo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/yurydemin/marchi/internal/db/writer"
	"github.com/yurydemin/marchi/internal/domain"
)

// RetentionSettingsRepo is the retention_settings table's repository — a
// singleton row (FR-RE-04), the same shape as S3ConfigRepo.
type RetentionSettingsRepo struct {
	db *sql.DB
	w  writer.Writer
}

func NewRetentionSettingsRepo(db *sql.DB, w writer.Writer) *RetentionSettingsRepo {
	return &RetentionSettingsRepo{db: db, w: w}
}

// Get returns the singleton row, or sql.ErrNoRows if no default has ever
// been set — the zero-config state where no email is subject to
// retention unless its account has its own override.
func (r *RetentionSettingsRepo) Get(ctx context.Context) (*domain.RetentionSettings, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT default_local_days, default_move_to_s3_days, default_s3_days, updated_at
		FROM retention_settings WHERE id = 1`)
	return scanRetentionSettings(row)
}

// Upsert writes the singleton row (id=1), creating it on first use and
// overwriting it entirely thereafter — same convention as S3ConfigRepo.Upsert.
func (r *RetentionSettingsRepo) Upsert(ctx context.Context, s *domain.RetentionSettings) error {
	return r.w.Do(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO retention_settings (id, default_local_days, default_move_to_s3_days, default_s3_days, updated_at)
			VALUES (1, ?, ?, ?, CURRENT_TIMESTAMP)
			ON CONFLICT(id) DO UPDATE SET
				default_local_days = excluded.default_local_days,
				default_move_to_s3_days = excluded.default_move_to_s3_days,
				default_s3_days = excluded.default_s3_days,
				updated_at = excluded.updated_at`,
			nullIfZeroPtr(s.DefaultLocalDays), nullIfZeroPtr(s.DefaultMoveToS3Days), nullIfZeroPtr(s.DefaultS3Days),
		)
		if err != nil {
			return fmt.Errorf("repo: upserting retention_settings: %w", err)
		}
		return nil
	})
}

func scanRetentionSettings(row rowScanner) (*domain.RetentionSettings, error) {
	var (
		s                                        domain.RetentionSettings
		defaultLocal, defaultMoveToS3, defaultS3 sql.NullInt64
		updatedAt                                string
	)
	err := row.Scan(&defaultLocal, &defaultMoveToS3, &defaultS3, &updatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		return nil, fmt.Errorf("repo: scanning retention_settings: %w", err)
	}
	s.DefaultLocalDays = nullInt64ToIntPtr(defaultLocal)
	s.DefaultMoveToS3Days = nullInt64ToIntPtr(defaultMoveToS3)
	s.DefaultS3Days = nullInt64ToIntPtr(defaultS3)

	s.UpdatedAt, err = parseSQLiteTime(updatedAt)
	if err != nil {
		return nil, fmt.Errorf("repo: parsing updated_at: %w", err)
	}
	return &s, nil
}
