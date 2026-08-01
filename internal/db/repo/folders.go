package repo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/yurydemin/marchi/internal/db/writer"
	"github.com/yurydemin/marchi/internal/domain"
)

// FoldersRepo is the folders table's repository.
type FoldersRepo struct {
	db *sql.DB
	w  writer.Writer
}

func NewFoldersRepo(db *sql.DB, w writer.Writer) *FoldersRepo {
	return &FoldersRepo{db: db, w: w}
}

// UpsertFolder records folderName's current UIDVALIDITY for accountID
// (FR-SE-01). A new folder is created with last_uid=0. An existing folder
// whose UIDVALIDITY changed has its last_uid reset to 0 too — the server
// has invalidated our UID bookkeeping, so the Sync Engine must treat this
// as a full resync (FR-SE-02). An existing folder whose UIDVALIDITY is
// unchanged keeps its last_uid untouched.
func (r *FoldersRepo) UpsertFolder(ctx context.Context, accountID int64, folderName string, uidValidity uint32) (*domain.Folder, error) {
	var f domain.Folder
	err := r.w.Do(ctx, func(tx *sql.Tx) error {
		row := tx.QueryRowContext(ctx, `
			INSERT INTO folders (account_id, folder_name, uidvalidity, last_uid, sync_enabled)
			VALUES (?, ?, ?, 0, 1)
			ON CONFLICT(account_id, folder_name) DO UPDATE SET
				uidvalidity = excluded.uidvalidity,
				last_uid = CASE
					WHEN folders.uidvalidity != excluded.uidvalidity THEN 0
					ELSE folders.last_uid
				END
			RETURNING id, account_id, folder_name, uidvalidity, last_uid, sync_enabled, msgraph_delta_link`,
			accountID, folderName, uidValidity,
		)
		var syncEnabled int
		var deltaLink sql.NullString
		if err := row.Scan(&f.ID, &f.AccountID, &f.FolderName, &f.UIDValidity, &f.LastUID, &syncEnabled, &deltaLink); err != nil {
			return fmt.Errorf("repo: upserting folder %q: %w", folderName, err)
		}
		f.SyncEnabled = syncEnabled != 0
		f.MSGraphDeltaLink = deltaLink.String
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// UpdateMSGraphDeltaLink replaces only folderID's msgraph_delta_link — the
// narrow write internal/sync.SyncAccountMSGraph needs after a folder's
// sync batch completes without error, mirroring
// AccountsRepo.UpdateGmailHistoryID's reasoning (a dedicated narrow
// write rather than folding into UpdateLastUID's per-message
// transaction, since the cursor only advances once per folder per run,
// not once per message).
func (r *FoldersRepo) UpdateMSGraphDeltaLink(ctx context.Context, folderID int64, deltaLink string) error {
	return r.w.Do(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE folders SET msgraph_delta_link = ? WHERE id = ?`, nullIfEmpty(deltaLink), folderID)
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

// UpdateLastUID advances folderID's last_uid within an existing
// transaction — for combining with other writes (inserting the email that
// uid refers to) into one atomic Single-Writer transaction, the pattern
// internal/db/writer's own doc comment calls out as the reason Do takes a
// closure instead of just running one statement.
//
// The "AND last_uid < ?" guard means this can never regress last_uid
// backward — relevant if messages are ever processed out of UID order,
// which nothing currently does, but costs nothing to guard against.
func (r *FoldersRepo) UpdateLastUID(ctx context.Context, tx *sql.Tx, folderID int64, uid uint32) error {
	if _, err := tx.ExecContext(ctx, `UPDATE folders SET last_uid = ? WHERE id = ? AND last_uid < ?`, uid, folderID, uid); err != nil {
		return fmt.Errorf("repo: updating folder %d last_uid: %w", folderID, err)
	}
	return nil
}

// GetOrCreateManual looks up accountID's folder named folderName within tx
// and returns it completely untouched if found. It deliberately does NOT go
// through UpsertFolder for the found case: UpsertFolder's ON CONFLICT resets
// last_uid to 0 whenever the passed uidValidity doesn't match what's
// stored, which would silently force a spurious full resync of a real,
// actively-synced folder if this were ever called against one (the bulk
// folder-move API has no real UIDVALIDITY to offer for a destination
// folder). Only when no folder exists yet under this name is a brand-new
// row created — uidvalidity=0/last_uid=0/sync_enabled=false, since it isn't
// tied to any live IMAP/Gmail/Graph source the Sync Engine will ever
// populate on its own.
//
// Callers must run this inside their own writer.Writer.Do transaction —
// the Single Writer Pattern's serialization is what makes the lookup+create
// here race-free against a concurrent live sync's own UpsertFolder call for
// the same (account_id, folder_name).
func (r *FoldersRepo) GetOrCreateManual(ctx context.Context, tx *sql.Tx, accountID int64, folderName string) (*domain.Folder, error) {
	f, err := r.getByAccountAndNameTx(ctx, tx, accountID, folderName)
	if err == nil {
		return f, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	var nf domain.Folder
	row := tx.QueryRowContext(ctx, `
		INSERT INTO folders (account_id, folder_name, uidvalidity, last_uid, sync_enabled)
		VALUES (?, ?, 0, 0, 0)
		RETURNING id, account_id, folder_name, uidvalidity, last_uid, sync_enabled, msgraph_delta_link`,
		accountID, folderName,
	)
	var syncEnabled int
	var deltaLink sql.NullString
	if err := row.Scan(&nf.ID, &nf.AccountID, &nf.FolderName, &nf.UIDValidity, &nf.LastUID, &syncEnabled, &deltaLink); err != nil {
		return nil, fmt.Errorf("repo: creating manual folder %q: %w", folderName, err)
	}
	nf.SyncEnabled = syncEnabled != 0
	nf.MSGraphDeltaLink = deltaLink.String
	return &nf, nil
}

func (r *FoldersRepo) getByAccountAndNameTx(ctx context.Context, tx *sql.Tx, accountID int64, folderName string) (*domain.Folder, error) {
	var f domain.Folder
	var syncEnabled int
	var deltaLink sql.NullString
	err := tx.QueryRowContext(ctx, `
		SELECT id, account_id, folder_name, uidvalidity, last_uid, sync_enabled, msgraph_delta_link
		FROM folders WHERE account_id = ? AND folder_name = ?`, accountID, folderName,
	).Scan(&f.ID, &f.AccountID, &f.FolderName, &f.UIDValidity, &f.LastUID, &syncEnabled, &deltaLink)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		return nil, fmt.Errorf("repo: getting folder %q for account %d: %w", folderName, accountID, err)
	}
	f.SyncEnabled = syncEnabled != 0
	f.MSGraphDeltaLink = deltaLink.String
	return &f, nil
}

// GetByID returns one folder by id, or sql.ErrNoRows.
func (r *FoldersRepo) GetByID(ctx context.Context, id int64) (*domain.Folder, error) {
	var f domain.Folder
	var syncEnabled int
	var deltaLink sql.NullString
	err := r.db.QueryRowContext(ctx, `
		SELECT id, account_id, folder_name, uidvalidity, last_uid, sync_enabled, msgraph_delta_link
		FROM folders WHERE id = ?`, id,
	).Scan(&f.ID, &f.AccountID, &f.FolderName, &f.UIDValidity, &f.LastUID, &syncEnabled, &deltaLink)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		return nil, fmt.Errorf("repo: getting folder %d: %w", id, err)
	}
	f.SyncEnabled = syncEnabled != 0
	f.MSGraphDeltaLink = deltaLink.String
	return &f, nil
}

// ListByAccount returns every folder recorded for accountID, alphabetically.
func (r *FoldersRepo) ListByAccount(ctx context.Context, accountID int64) ([]*domain.Folder, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, account_id, folder_name, uidvalidity, last_uid, sync_enabled, msgraph_delta_link
		FROM folders WHERE account_id = ? ORDER BY folder_name`, accountID)
	if err != nil {
		return nil, fmt.Errorf("repo: listing folders: %w", err)
	}
	defer rows.Close()

	var folders []*domain.Folder
	for rows.Next() {
		var f domain.Folder
		var syncEnabled int
		var deltaLink sql.NullString
		if err := rows.Scan(&f.ID, &f.AccountID, &f.FolderName, &f.UIDValidity, &f.LastUID, &syncEnabled, &deltaLink); err != nil {
			return nil, fmt.Errorf("repo: scanning folder: %w", err)
		}
		f.SyncEnabled = syncEnabled != 0
		f.MSGraphDeltaLink = deltaLink.String
		folders = append(folders, &f)
	}
	return folders, rows.Err()
}
