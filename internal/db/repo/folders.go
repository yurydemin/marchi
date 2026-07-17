package repo

import (
	"context"
	"database/sql"
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
			RETURNING id, account_id, folder_name, uidvalidity, last_uid, sync_enabled`,
			accountID, folderName, uidValidity,
		)
		var syncEnabled int
		if err := row.Scan(&f.ID, &f.AccountID, &f.FolderName, &f.UIDValidity, &f.LastUID, &syncEnabled); err != nil {
			return fmt.Errorf("repo: upserting folder %q: %w", folderName, err)
		}
		f.SyncEnabled = syncEnabled != 0
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// ListByAccount returns every folder recorded for accountID, alphabetically.
func (r *FoldersRepo) ListByAccount(ctx context.Context, accountID int64) ([]*domain.Folder, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, account_id, folder_name, uidvalidity, last_uid, sync_enabled
		FROM folders WHERE account_id = ? ORDER BY folder_name`, accountID)
	if err != nil {
		return nil, fmt.Errorf("repo: listing folders: %w", err)
	}
	defer rows.Close()

	var folders []*domain.Folder
	for rows.Next() {
		var f domain.Folder
		var syncEnabled int
		if err := rows.Scan(&f.ID, &f.AccountID, &f.FolderName, &f.UIDValidity, &f.LastUID, &syncEnabled); err != nil {
			return nil, fmt.Errorf("repo: scanning folder: %w", err)
		}
		f.SyncEnabled = syncEnabled != 0
		folders = append(folders, &f)
	}
	return folders, rows.Err()
}
