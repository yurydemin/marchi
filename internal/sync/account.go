package sync

import (
	"context"
	"fmt"

	"github.com/yurydemin/marchi/internal/db/repo"
	"github.com/yurydemin/marchi/internal/db/writer"
	"github.com/yurydemin/marchi/internal/domain"
	"github.com/yurydemin/marchi/internal/imapclient"
	"github.com/yurydemin/marchi/internal/maildir"
)

// FolderResult summarizes one folder's sync-then-fetch pass.
type FolderResult struct {
	Folder  *domain.Folder
	Fetched int
}

// SyncAccount connects to a using the already-decrypted password, syncs its
// folder list (FR-SE-01), then fetches new messages for every folder into
// maildirRoot (config.yaml's storage.maildir_path). host names the Maildir
// filename's hostname component (see maildir.NewWriter).
//
// A folder-level fetch error stops that folder (see FetchNewMessages'
// own doc comment on why) but does not abort the remaining folders — one
// folder having trouble shouldn't block archiving the rest of the account.
// The first error encountered, if any, is returned alongside every result
// gathered up to that point.
func SyncAccount(
	ctx context.Context,
	a *domain.Account,
	password string,
	maildirRoot, host string,
	w writer.Writer,
	foldersRepo *repo.FoldersRepo,
	emailsRepo *repo.EmailsRepo,
) ([]FolderResult, error) {
	c, err := imapclient.Connect(ctx, imapclient.ConnectOptions{
		Host:     a.IMAPHost,
		Port:     a.IMAPPort,
		TLS:      a.IMAPTLS,
		Username: a.IMAPUsername,
		Password: password,
	})
	if err != nil {
		return nil, err
	}
	defer c.Logout()

	folders, err := SyncFolders(ctx, c, a.ID, foldersRepo)
	if err != nil {
		return nil, err
	}

	results := make([]FolderResult, 0, len(folders))
	var firstErr error
	for _, folder := range folders {
		if firstErr != nil {
			results = append(results, FolderResult{Folder: folder})
			continue
		}

		dir := maildir.FolderDir(maildirRoot, a.ID, maildir.SafeFolderName(folder.FolderName))
		layout, layoutErr := maildir.NewLayout(dir)
		if layoutErr != nil {
			firstErr = fmt.Errorf("sync: preparing maildir for %q: %w", folder.FolderName, layoutErr)
			results = append(results, FolderResult{Folder: folder})
			continue
		}
		mw := maildir.NewWriter(layout, host)

		fetched, fetchErr := FetchNewMessages(ctx, c, a.ID, folder, mw, w, emailsRepo, foldersRepo)
		results = append(results, FolderResult{Folder: folder, Fetched: fetched})
		if fetchErr != nil {
			firstErr = fmt.Errorf("sync: fetching %q: %w", folder.FolderName, fetchErr)
		}
	}
	return results, firstErr
}
