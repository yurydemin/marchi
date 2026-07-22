package main

import (
	"fmt"

	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"github.com/yurydemin/marchi/internal/db"
	"github.com/yurydemin/marchi/internal/i18n"
)

func newMigrateCmd(loc *i18n.Localizer) *cobra.Command {
	return &cobra.Command{
		Use:   "migrate",
		Short: loc.T("cli.migrate.short"),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := configFrom(cmd.Context())
			logger := loggerFrom(cmd.Context())

			sqlDB, err := db.Open(cfg.Database.SQLite.Path)
			if err != nil {
				return err
			}
			defer closeDB(logger, sqlDB)

			logger.Info("migrations applied", zap.String("path", cfg.Database.SQLite.Path))
			fmt.Fprintf(cmd.OutOrStdout(), "Migrations applied: %s\n", cfg.Database.SQLite.Path)
			return nil
		},
	}
}
