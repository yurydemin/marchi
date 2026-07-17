package sync

import (
	"context"

	"github.com/yurydemin/marchi/internal/db/repo"
	"github.com/yurydemin/marchi/internal/domain"
	"github.com/yurydemin/marchi/internal/imapclient"
)

// SyncAccount connects to a using the already-decrypted password and syncs
// its folder list. Email fetch/archiving is a later step — this is exactly
// FR-SE-01's UIDVALIDITY/UID bookkeeping and nothing else yet.
func SyncAccount(ctx context.Context, a *domain.Account, password string, foldersRepo *repo.FoldersRepo) ([]*domain.Folder, error) {
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

	return SyncFolders(ctx, c, a.ID, foldersRepo)
}
