package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/yurydemin/marchi/internal/db"
	"github.com/yurydemin/marchi/internal/db/repo"
	"github.com/yurydemin/marchi/internal/db/writer"
	"github.com/yurydemin/marchi/internal/i18n"
	"github.com/yurydemin/marchi/internal/retention"
	"github.com/yurydemin/marchi/internal/s3config"
	"github.com/yurydemin/marchi/internal/search"
)

func newRetentionCmd(loc *i18n.Localizer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "retention",
		Short: loc.T("cli.retention.short"),
	}
	cmd.AddCommand(newRetentionRunCmd(loc))
	return cmd
}

// newRetentionRunCmd runs one retention pass immediately — the same pass
// the Scheduler's daily cron (internal/scheduler) runs automatically, but
// callable on demand for manual runs or testing without waiting for
// 03:00. Needs the Master Key because Stage B->C (deleting from S3)
// requires decrypting the saved S3 credentials.
func newRetentionRunCmd(loc *i18n.Localizer) *cobra.Command {
	return &cobra.Command{
		Use:   "run",
		Short: loc.T("cli.retention_run.short"),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := configFrom(cmd.Context())
			logger := loggerFrom(cmd.Context())

			masterKey, err := unlockMasterKey(cfg, logger)
			if err != nil {
				return err
			}

			sqlDB, err := db.Open(cfg.Database.SQLite.Path)
			if err != nil {
				return err
			}
			defer closeDB(logger, sqlDB)

			w := writer.New(sqlDB)
			defer w.Close()

			accountsRepo := repo.NewAccountsRepo(sqlDB, w)
			emailsRepo := repo.NewEmailsRepo(sqlDB, w)
			retentionSettingsRepo := repo.NewRetentionSettingsRepo(sqlDB, w)
			s3ConfigRepo := repo.NewS3ConfigRepo(sqlDB, w)

			s3ConfigMgr, err := s3config.NewManager(s3ConfigRepo, masterKey)
			if err != nil {
				return err
			}

			idx, err := search.Open(cfg.Search.IndexPath)
			if err != nil {
				return err
			}
			defer idx.Close()

			runner := retention.New(retention.Deps{
				AccountsRepo: accountsRepo, EmailsRepo: emailsRepo,
				RetentionSettingsRepo: retentionSettingsRepo, S3ConfigRepo: s3ConfigRepo,
				S3ConfigManager: s3ConfigMgr, Writer: w,
				IndexFunc: func() *search.Index { return idx },
				Logger:    logger,
			})

			stats, err := runner.Run(cmd.Context())
			if err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Retention run complete: %d moved to S3-only, %d deleted directly (no S3 backup), %d deleted from S3, %d error(s).\n",
				stats.MovedToS3Only, stats.DeletedDirect, stats.DeletedFromS3, stats.Errors)
			return nil
		},
	}
}
