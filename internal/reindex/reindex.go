// Package reindex implements FR-SR-04: wiping and rebuilding the search
// index from the local .eml archive — the recovery path whenever the
// index falls out of sync with the archive (best-effort indexing failures
// during sync, per internal/sync's archiveOne, or restoring from a backup
// that didn't include the index, since FR-SR-01 never replicates it).
//
// Reindexing re-parses each email's .eml from scratch (mimeparse) rather
// than trusting the emails table's already-extracted columns — the .eml
// file is the actual source of truth FR-SR-04 names ("перечитывает все
// локальные .eml"), and re-deriving independently also catches any case
// where the index and the SQLite row have quietly diverged.
//
// Running this CLI command concurrently with the long-running server (an
// httpapi backend, which holds its own open Bluge writer on the same
// index path) is not supported: removing and recreating the index
// directory out from under another process's open writer leaves that
// writer working against unlinked files. Operators should stop the server
// before reindexing from the CLI.
package reindex

import (
	"context"
	"fmt"
	"os"

	"github.com/yurydemin/marchi/internal/db/repo"
	"github.com/yurydemin/marchi/internal/domain"
	"github.com/yurydemin/marchi/internal/mimeparse"
	"github.com/yurydemin/marchi/internal/search"
)

// Stats summarizes one reindex run.
type Stats struct {
	Total   int // emails considered
	Indexed int // successfully (re)indexed
	Skipped int // not locally archived (e.g. S3-only once Phase 3 exists), so nothing to re-parse
	Errors  int // local file present but unreadable, or the index write itself failed
}

// Run deletes indexPath's existing Bluge index (FR-SR-04: "Удаляет
// текущий индекс") and rebuilds it from every email's local .eml
// (emailsRepo.ListAll). It always returns a usable *search.Index open at
// indexPath — even a partial or empty one on error — since the old index
// is already gone by the time any error here could occur; the caller
// owns closing it regardless of the returned error.
func Run(ctx context.Context, emailsRepo *repo.EmailsRepo, indexPath string) (*search.Index, Stats, error) {
	if err := os.RemoveAll(indexPath); err != nil {
		return nil, Stats{}, fmt.Errorf("reindex: removing existing index at %s: %w", indexPath, err)
	}

	idx, err := search.Open(indexPath)
	if err != nil {
		return nil, Stats{}, fmt.Errorf("reindex: opening fresh index: %w", err)
	}

	emails, err := emailsRepo.ListAll(ctx)
	if err != nil {
		return idx, Stats{}, fmt.Errorf("reindex: listing emails: %w", err)
	}

	var stats Stats
	stats.Total = len(emails)
	for _, e := range emails {
		if err := ctx.Err(); err != nil {
			return idx, stats, err
		}
		if e.StorageLocation != "local" || e.LocalPath == "" {
			stats.Skipped++
			continue
		}
		if err := indexOne(idx, e); err != nil {
			stats.Errors++
			continue
		}
		stats.Indexed++
	}
	return idx, stats, nil
}

func indexOne(idx *search.Index, e *domain.Email) error {
	raw, err := os.ReadFile(e.LocalPath)
	if err != nil {
		return fmt.Errorf("reindex: reading %s: %w", e.LocalPath, err)
	}

	md := mimeparse.Parse(raw)
	attachments := mimeparse.ParseAttachments(raw)
	names := make([]string, len(attachments))
	for i, a := range attachments {
		names[i] = a.Filename
	}

	return idx.Index(search.Doc{
		EmailID:         e.ID,
		MessageID:       md.MessageID,
		Subject:         md.Subject,
		From:            md.From,
		FromAddr:        md.FromAddr,
		To:              md.To,
		ToAddrs:         md.ToAddrs,
		Cc:              md.Cc,
		CcAddrs:         md.CcAddrs,
		Body:            mimeparse.ParseBody(raw),
		AttachmentNames: names,
		Date:            md.Date,
		AccountID:       e.AccountID,
		FolderID:        e.FolderID,
		HasAttachments:  len(attachments) > 0,
		Size:            int64(len(raw)),
	})
}
