package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/yurydemin/marchi/internal/account"
	"github.com/yurydemin/marchi/internal/db"
	"github.com/yurydemin/marchi/internal/db/repo"
	"github.com/yurydemin/marchi/internal/db/writer"
	"github.com/yurydemin/marchi/internal/search"
	syncengine "github.com/yurydemin/marchi/internal/sync"
)

func newSyncCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "sync <email>",
		Short: "Sync an account: folder list (FR-SE-01) then new messages in each folder",
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
			defer closeDB(logger, sqlDB)

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

			host, err := os.Hostname()
			if err != nil {
				host = "localhost"
			}

			idx, err := search.Open(cfg.Search.IndexPath)
			if err != nil {
				return err
			}
			defer idx.Close()

			foldersRepo := repo.NewFoldersRepo(sqlDB, w)
			emailsRepo := repo.NewEmailsRepo(sqlDB, w)
			attachmentsRepo := repo.NewAttachmentsRepo(sqlDB, w)
			syncLogsRepo := repo.NewSyncLogsRepo(sqlDB, w)
			results, err := syncengine.SyncAccount(cmd.Context(), a, password, cfg.Storage.MaildirPath, host, w, foldersRepo, emailsRepo, attachmentsRepo, syncLogsRepo, idx, nil)

			total := 0
			for _, r := range results {
				total += r.Fetched
				fmt.Fprintf(cmd.OutOrStdout(), "  %-30s uidvalidity=%-12d last_uid=%-8d fetched=%d\n",
					r.Folder.FolderName, r.Folder.UIDValidity, r.Folder.LastUID, r.Fetched)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Synced %s: %d folder(s), %d new message(s) archived.\n", email, len(results), total)

			return err
		},
	}
}
