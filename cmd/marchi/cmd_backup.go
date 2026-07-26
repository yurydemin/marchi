package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/yurydemin/marchi/internal/backup"
	"github.com/yurydemin/marchi/internal/db"
	"github.com/yurydemin/marchi/internal/i18n"
)

func newBackupCmd(loc *i18n.Localizer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backup",
		Short: loc.T("cli.backup.short"),
	}
	cmd.AddCommand(newBackupRunCmd(loc))
	cmd.AddCommand(newBackupVerifyCmd(loc))
	return cmd
}

// newBackupRunCmd writes a full backup (database, Maildir, Master Key
// material) into a fresh directory. It doesn't need the Master Key
// itself — nothing in a backup is ever decrypted, .salt/.mk-verify/.dek
// are copied as opaque ciphertext, and the database's own encrypted
// columns stay encrypted in the copy — so this runs unattended, without a
// password prompt, exactly like a cron job needs to.
func newBackupRunCmd(loc *i18n.Localizer) *cobra.Command {
	return &cobra.Command{
		Use:   "run <dest-dir>",
		Short: loc.T("cli.backup_run.short"),
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := configFrom(cmd.Context())
			logger := loggerFrom(cmd.Context())
			destDir := args[0]

			sqlDB, err := db.Open(cfg.Database.SQLite.Path)
			if err != nil {
				return err
			}
			defer closeDB(logger, sqlDB)

			manifest, err := backup.Run(cmd.Context(), sqlDB, cfg.App.DataDir, cfg.Storage.MaildirPath, destDir)
			if err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Backup complete: %s (%d files, marchi %s, %s)\n",
				destDir, len(manifest.Files), manifest.MarchiVersion, manifest.CreatedAt.Format("2006-01-02T15:04:05Z"))
			return nil
		},
	}
}

// newBackupVerifyCmd re-checks a backup written by `backup run` — every
// manifest entry's SHA-256 still matches on disk, and the backed-up
// database itself passes PRAGMA integrity_check. Doesn't need the vault
// or the Master Key either, for the same reason `run` doesn't.
func newBackupVerifyCmd(loc *i18n.Localizer) *cobra.Command {
	return &cobra.Command{
		Use:   "verify <dir>",
		Short: loc.T("cli.backup_verify.short"),
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := backup.Verify(args[0])
			if err != nil {
				return err
			}

			if !result.OK {
				fmt.Fprintf(cmd.OutOrStdout(), "Backup verification FAILED: mismatched files=%v, integrity_check=%q\n",
					result.MismatchedFiles, result.IntegrityCheckMsg)
				return fmt.Errorf("backup verification failed")
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Backup verified OK (integrity_check: %s)\n", result.IntegrityCheckMsg)
			return nil
		},
	}
}
