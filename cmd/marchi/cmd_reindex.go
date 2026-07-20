package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/yurydemin/marchi/internal/db"
	"github.com/yurydemin/marchi/internal/db/repo"
	"github.com/yurydemin/marchi/internal/reindex"
)

// newReindexCmd deliberately doesn't call unlockMasterKey — a reindex only
// touches the (unencrypted) emails table and the local .eml files
// themselves, never any credential, so there's nothing here that actually
// needs the Master Key.
func newReindexCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reindex",
		Short: "Rebuild the full-text search index from the local .eml archive (FR-SR-04)",
		Long: "Rebuild the full-text search index from the local .eml archive (FR-SR-04).\n" +
			"Do not run this while the server (`marchi` with no subcommand) is running against\n" +
			"the same data directory — it deletes and recreates the index directory on disk, which\n" +
			"a concurrently open server process won't see.",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := configFrom(cmd.Context())
			logger := loggerFrom(cmd.Context())

			sqlDB, err := db.Open(cfg.Database.SQLite.Path)
			if err != nil {
				return err
			}
			defer closeDB(logger, sqlDB)

			emailsRepo := repo.NewEmailsRepo(sqlDB, nil) // read-only: ListAll never writes

			idx, stats, err := reindex.Run(cmd.Context(), emailsRepo, cfg.Search.IndexPath, nil)
			if idx != nil {
				defer idx.Close()
			}
			if err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Reindex complete: %d email(s) total, %d indexed, %d skipped, %d error(s).\n",
				stats.Total, stats.Indexed, stats.Skipped, stats.Errors)
			return nil
		},
	}
}
