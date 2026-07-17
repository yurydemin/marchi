package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/yurydemin/marchi/internal/account"
	"github.com/yurydemin/marchi/internal/db"
	"github.com/yurydemin/marchi/internal/db/repo"
	"github.com/yurydemin/marchi/internal/db/writer"
	syncengine "github.com/yurydemin/marchi/internal/sync"
)

func newSyncCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "sync <email>",
		Short: "Sync an account's folder list — UIDVALIDITY/last_uid bookkeeping (FR-SE-01); message fetch comes in a later step",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			email := args[0]
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
			defer sqlDB.Close()

			w := writer.New(sqlDB)
			defer w.Close()

			accountsRepo := repo.NewAccountsRepo(sqlDB, w)
			mgr, err := account.NewManager(accountsRepo, masterKey)
			if err != nil {
				return err
			}

			a, err := accountsRepo.GetByEmail(cmd.Context(), email)
			if err != nil {
				return fmt.Errorf("account %s not found: %w", email, err)
			}

			password, err := mgr.DecryptPassword(a)
			if err != nil {
				return err
			}

			foldersRepo := repo.NewFoldersRepo(sqlDB, w)
			folders, err := syncengine.SyncAccount(cmd.Context(), a, password, foldersRepo)
			if err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Synced %d folder(s) for %s:\n", len(folders), email)
			for _, f := range folders {
				fmt.Fprintf(cmd.OutOrStdout(), "  %-30s uidvalidity=%d last_uid=%d\n", f.FolderName, f.UIDValidity, f.LastUID)
			}
			return nil
		},
	}
}
