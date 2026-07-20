package repo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/yurydemin/marchi/internal/db/writer"
	"github.com/yurydemin/marchi/internal/domain"
)

// OAuth2AppsRepo is the oauth2_apps table's repository — one row per
// provider (поправка #4's BYO app), unlike S3ConfigRepo/
// RetentionSettingsRepo's singleton shape.
type OAuth2AppsRepo struct {
	db *sql.DB
	w  writer.Writer
}

func NewOAuth2AppsRepo(db *sql.DB, w writer.Writer) *OAuth2AppsRepo {
	return &OAuth2AppsRepo{db: db, w: w}
}

// Get returns provider's BYO app config, or sql.ErrNoRows if the user
// hasn't registered one yet.
func (r *OAuth2AppsRepo) Get(ctx context.Context, provider string) (*domain.OAuth2App, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT provider, client_id, client_secret_encrypted, redirect_url, updated_at
		FROM oauth2_apps WHERE provider = ?`, provider)
	return scanOAuth2App(row)
}

// List returns every configured provider's app — the Settings UI's view
// of "which providers are ready to use".
func (r *OAuth2AppsRepo) List(ctx context.Context) ([]*domain.OAuth2App, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT provider, client_id, client_secret_encrypted, redirect_url, updated_at
		FROM oauth2_apps ORDER BY provider`)
	if err != nil {
		return nil, fmt.Errorf("repo: listing oauth2 apps: %w", err)
	}
	defer rows.Close()

	var apps []*domain.OAuth2App
	for rows.Next() {
		app, err := scanOAuth2App(rows)
		if err != nil {
			return nil, err
		}
		apps = append(apps, app)
	}
	return apps, rows.Err()
}

// Upsert writes provider's row, creating it on first use and overwriting
// it entirely thereafter — same convention as S3ConfigRepo.Upsert, just
// keyed by provider instead of a fixed id=1.
func (r *OAuth2AppsRepo) Upsert(ctx context.Context, app *domain.OAuth2App) error {
	return r.w.Do(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO oauth2_apps (provider, client_id, client_secret_encrypted, redirect_url, updated_at)
			VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP)
			ON CONFLICT(provider) DO UPDATE SET
				client_id = excluded.client_id,
				client_secret_encrypted = excluded.client_secret_encrypted,
				redirect_url = excluded.redirect_url,
				updated_at = excluded.updated_at`,
			app.Provider, app.ClientID, app.ClientSecretEncrypted, app.RedirectURL,
		)
		if err != nil {
			return fmt.Errorf("repo: upserting oauth2_apps[%s]: %w", app.Provider, err)
		}
		return nil
	})
}

func scanOAuth2App(row rowScanner) (*domain.OAuth2App, error) {
	var (
		app       domain.OAuth2App
		updatedAt string
	)
	err := row.Scan(&app.Provider, &app.ClientID, &app.ClientSecretEncrypted, &app.RedirectURL, &updatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		return nil, fmt.Errorf("repo: scanning oauth2_apps: %w", err)
	}
	app.UpdatedAt, err = parseSQLiteTime(updatedAt)
	if err != nil {
		return nil, fmt.Errorf("repo: parsing updated_at: %w", err)
	}
	return &app, nil
}
