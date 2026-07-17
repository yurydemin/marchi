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
				oauth2_provider, oauth2_token_encrypted, is_active
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			a.Email, nullIfEmpty(a.DisplayName), a.IMAPHost, a.IMAPPort, int(a.IMAPTLS),
			nullIfEmpty(a.IMAPUsername), a.IMAPPasswordEncrypted,
			nullIfEmpty(a.OAuth2Provider), a.OAuth2TokenEncrypted, boolToInt(a.IsActive),
		)
		if err != nil {
			return wrapAccountErr(err)
		}
		id, err = res.LastInsertId()
		return err
	})
	return id, err
}

// List returns all accounts ordered by id.
func (r *AccountsRepo) List(ctx context.Context) ([]*domain.Account, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, email, display_name, imap_host, imap_port, imap_tls,
		       imap_username, imap_password_encrypted,
		       oauth2_provider, oauth2_token_encrypted, is_active,
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

// GetByEmail returns the account with the given email, or sql.ErrNoRows.
func (r *AccountsRepo) GetByEmail(ctx context.Context, email string) (*domain.Account, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, email, display_name, imap_host, imap_port, imap_tls,
		       imap_username, imap_password_encrypted,
		       oauth2_provider, oauth2_token_encrypted, is_active,
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
		a                     domain.Account
		displayName, imapUser sql.NullString
		oauth2Provider        sql.NullString
		isActive              int
		imapTLS               int
		createdAt, updatedAt  string
	)
	err := row.Scan(
		&a.ID, &a.Email, &displayName, &a.IMAPHost, &a.IMAPPort, &imapTLS,
		&imapUser, &a.IMAPPasswordEncrypted,
		&oauth2Provider, &a.OAuth2TokenEncrypted, &isActive,
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
	a.IMAPTLS = domain.IMAPTLSMode(imapTLS)
	a.IsActive = isActive != 0

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

func wrapAccountErr(err error) error {
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed: accounts.email") {
		return ErrDuplicateEmail
	}
	return err
}
