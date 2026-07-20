// Package repo holds per-table data access: reads go straight against the
// pooled *sql.DB (WAL allows concurrent readers), writes go through
// writer.Writer so every mutation is serialized through the Single Writer
// Pattern (FR-ST-02). Repos never issue write transactions against *sql.DB
// directly.
package repo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/yurydemin/marchi/internal/db/writer"
	"github.com/yurydemin/marchi/internal/domain"
)

// ErrDuplicateEmail is returned by Create when accounts.email's UNIQUE
// constraint rejects the insert (FR-AM-01: email is the account's unique
// identifier).
var ErrDuplicateEmail = errors.New("repo: account with this email already exists")

const sqliteTimeLayout = "2006-01-02 15:04:05"

// AccountsRepo is the accounts table's repository. w may be nil if only
// read methods (List, GetByEmail) will ever be called on this instance —
// mutating methods dereference it and will panic if it's missing.
type AccountsRepo struct {
	db *sql.DB
	w  writer.Writer
}

func NewAccountsRepo(db *sql.DB, w writer.Writer) *AccountsRepo {
	return &AccountsRepo{db: db, w: w}
}

// Create inserts a new account and returns its assigned ID. a's
// credential fields are expected to already be encrypted — this repo has
// no notion of the Master Key (that's internal/account's job).
func (r *AccountsRepo) Create(ctx context.Context, a *domain.Account) (int64, error) {
	var id int64
	err := r.w.Do(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO accounts (
				email, display_name, imap_host, imap_port, imap_tls,
				imap_username, imap_password_encrypted,
				oauth2_provider, oauth2_token_encrypted, is_active,
				retention_local_days, retention_move_to_s3_days, retention_s3_days
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			a.Email, nullIfEmpty(a.DisplayName), a.IMAPHost, a.IMAPPort, int(a.IMAPTLS),
			nullIfEmpty(a.IMAPUsername), a.IMAPPasswordEncrypted,
			nullIfEmpty(a.OAuth2Provider), a.OAuth2TokenEncrypted, boolToInt(a.IsActive),
			nullIfZeroPtr(a.RetentionLocalDays), nullIfZeroPtr(a.RetentionMoveToS3Days), nullIfZeroPtr(a.RetentionS3Days),
		)
		if err != nil {
			return wrapAccountErr(err)
		}
		id, err = res.LastInsertId()
		return err
	})
	return id, err
}

// Update replaces every mutable column of the account identified by a.ID
// (email and id itself are not updatable — email is the account's stable
// identifier). Like Create, credential fields are expected to already be
// encrypted.
func (r *AccountsRepo) Update(ctx context.Context, a *domain.Account) error {
	return r.w.Do(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE accounts SET
				display_name = ?, imap_host = ?, imap_port = ?, imap_tls = ?,
				imap_username = ?, imap_password_encrypted = ?,
				is_active = ?, sync_cron = ?,
				retention_local_days = ?, retention_move_to_s3_days = ?, retention_s3_days = ?,
				updated_at = CURRENT_TIMESTAMP
			WHERE id = ?`,
			nullIfEmpty(a.DisplayName), a.IMAPHost, a.IMAPPort, int(a.IMAPTLS),
			nullIfEmpty(a.IMAPUsername), a.IMAPPasswordEncrypted,
			boolToInt(a.IsActive), nullIfEmpty(a.SyncCron),
			nullIfZeroPtr(a.RetentionLocalDays), nullIfZeroPtr(a.RetentionMoveToS3Days), nullIfZeroPtr(a.RetentionS3Days),
			a.ID,
		)
		if err != nil {
			return wrapAccountErr(err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return sql.ErrNoRows
		}
		return nil
	})
}

// UpdateOAuth2Token replaces only oauth2_token_encrypted — the narrow
// write a token refresh needs (internal/account.Manager.UpdateOAuth2Token),
// as opposed to Update's full-row replace which would require the caller
// to resupply every other field just to rotate a token.
func (r *AccountsRepo) UpdateOAuth2Token(ctx context.Context, id int64, tokenEncrypted []byte) error {
	return r.w.Do(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE accounts SET oauth2_token_encrypted = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
			tokenEncrypted, id)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return sql.ErrNoRows
		}
		return nil
	})
}

// Delete removes the account identified by id. Every row that references
// it (folders, emails, attachments, sync_logs) cascades via the schema's
// own ON DELETE CASCADE foreign keys (FR-AM-06) — callers that also need
// to clean up the search index or the on-disk Maildir archive (neither of
// which SQLite's foreign keys can reach) must do so themselves, before
// calling Delete, while the email rows this needs are still there to list.
func (r *AccountsRepo) Delete(ctx context.Context, id int64) error {
	return r.w.Do(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM accounts WHERE id = ?`, id)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return sql.ErrNoRows
		}
		return nil
	})
}

// List returns all accounts ordered by id.
func (r *AccountsRepo) List(ctx context.Context) ([]*domain.Account, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, email, display_name, imap_host, imap_port, imap_tls,
		       imap_username, imap_password_encrypted,
		       oauth2_provider, oauth2_token_encrypted, is_active, sync_cron,
		       retention_local_days, retention_move_to_s3_days, retention_s3_days,
		       created_at, updated_at
		FROM accounts ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("repo: listing accounts: %w", err)
	}
	defer rows.Close()

	var accounts []*domain.Account
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		accounts = append(accounts, a)
	}
	return accounts, rows.Err()
}

// GetByID returns the account with the given id, or sql.ErrNoRows.
func (r *AccountsRepo) GetByID(ctx context.Context, id int64) (*domain.Account, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, email, display_name, imap_host, imap_port, imap_tls,
		       imap_username, imap_password_encrypted,
		       oauth2_provider, oauth2_token_encrypted, is_active, sync_cron,
		       retention_local_days, retention_move_to_s3_days, retention_s3_days,
		       created_at, updated_at
		FROM accounts WHERE id = ?`, id)
	return scanAccount(row)
}

// GetByEmail returns the account with the given email, or sql.ErrNoRows.
func (r *AccountsRepo) GetByEmail(ctx context.Context, email string) (*domain.Account, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, email, display_name, imap_host, imap_port, imap_tls,
		       imap_username, imap_password_encrypted,
		       oauth2_provider, oauth2_token_encrypted, is_active, sync_cron,
		       retention_local_days, retention_move_to_s3_days, retention_s3_days,
		       created_at, updated_at
		FROM accounts WHERE email = ?`, email)
	return scanAccount(row)
}

// rowScanner is satisfied by both *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanAccount(row rowScanner) (*domain.Account, error) {
	var (
		a                                              domain.Account
		displayName, imapUser                          sql.NullString
		oauth2Provider                                 sql.NullString
		syncCron                                       sql.NullString
		retentionLocal, retentionMoveToS3, retentionS3 sql.NullInt64
		isActive                                       int
		imapTLS                                        int
		createdAt, updatedAt                           string
	)
	err := row.Scan(
		&a.ID, &a.Email, &displayName, &a.IMAPHost, &a.IMAPPort, &imapTLS,
		&imapUser, &a.IMAPPasswordEncrypted,
		&oauth2Provider, &a.OAuth2TokenEncrypted, &isActive, &syncCron,
		&retentionLocal, &retentionMoveToS3, &retentionS3,
		&createdAt, &updatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		return nil, fmt.Errorf("repo: scanning account: %w", err)
	}

	a.DisplayName = displayName.String
	a.IMAPUsername = imapUser.String
	a.OAuth2Provider = oauth2Provider.String
	a.SyncCron = syncCron.String
	a.IMAPTLS = domain.IMAPTLSMode(imapTLS)
	a.IsActive = isActive != 0
	a.RetentionLocalDays = nullInt64ToIntPtr(retentionLocal)
	a.RetentionMoveToS3Days = nullInt64ToIntPtr(retentionMoveToS3)
	a.RetentionS3Days = nullInt64ToIntPtr(retentionS3)

	a.CreatedAt, err = parseSQLiteTime(createdAt)
	if err != nil {
		return nil, fmt.Errorf("repo: parsing created_at: %w", err)
	}
	a.UpdatedAt, err = parseSQLiteTime(updatedAt)
	if err != nil {
		return nil, fmt.Errorf("repo: parsing updated_at: %w", err)
	}

	return &a, nil
}

func parseSQLiteTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(sqliteTimeLayout, s); err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339, s)
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// nullIfZeroPtr/nullInt64ToIntPtr are the shared (SQL NULL) <-> (*int)
// convention every nullable day-count column in this package uses
// (accounts' retention overrides, retention_settings' defaults) — nil
// means "no value" (SQL NULL), not the zero value 0 days, which is itself
// a meaningful "evict immediately" setting distinct from "unset".
func nullIfZeroPtr(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

func nullInt64ToIntPtr(n sql.NullInt64) *int {
	if !n.Valid {
		return nil
	}
	v := int(n.Int64)
	return &v
}

func wrapAccountErr(err error) error {
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed: accounts.email") {
		return ErrDuplicateEmail
	}
	return err
}
