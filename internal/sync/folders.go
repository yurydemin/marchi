// Package sync is the Sync Engine: orchestrating IMAP folder/UID
// bookkeeping (this step) and, from a later step, incremental message
// fetch and archiving.
package sync

import (
	"context"
	"fmt"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"

	"github.com/yurydemin/marchi/internal/db/repo"
	"github.com/yurydemin/marchi/internal/domain"
	"github.com/yurydemin/marchi/internal/imapclient"
)

// SyncFolders lists every folder on the already-connected, already-logged-in
// client, reads each one's UIDVALIDITY via STATUS (FR-SE-01) — not SELECT,
// which would also open the mailbox for message access we don't need yet —
// and upserts it into foldersRepo. It does not fetch any messages.
//
// \Noselect folders (pure path nodes some servers report, not real
// mailboxes — e.g. a "[Gmail]" separator entry) are skipped: STATUS on one
// of those fails server-side, and there'd be nothing to sync there anyway.
func SyncFolders(ctx context.Context, c *client.Client, accountID int64, foldersRepo *repo.FoldersRepo) ([]*domain.Folder, error) {
	remoteFolders, err := imapclient.ListFolders(c)
	if err != nil {
		return nil, err
	}

	result := make([]*domain.Folder, 0, len(remoteFolders))
	for _, rf := range remoteFolders {
		if hasAttribute(rf.Attributes, imap.NoSelectAttr) {
			continue
		}

		status, err := c.Status(rf.RawName, []imap.StatusItem{imap.StatusUidValidity})
		if err != nil {
			return nil, fmt.Errorf("sync: STATUS %q: %w", rf.Name, err)
		}

		f, err := foldersRepo.UpsertFolder(ctx, accountID, rf.Name, status.UidValidity)
		if err != nil {
			return nil, err
		}
		result = append(result, f)
	}
	return result, nil
}

func hasAttribute(attrs []string, want string) bool {
	for _, a := range attrs {
		if a == want {
			return true
		}
	}
	return false
}
