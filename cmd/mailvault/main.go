// Command mailvault is the single-binary entry point for the MailVault email archiving service.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"github.com/yurydemin/marchi/internal/config"
	"github.com/yurydemin/marchi/internal/logging"
	"github.com/yurydemin/marchi/internal/maildir"
	"github.com/yurydemin/marchi/internal/version"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	var closeLogging func() error

	root := &cobra.Command{
		Use:           "mailvault",
		Short:         "MailVault — self-hosted email archiving service",
		SilenceUsage:  true,
		SilenceErrors: false,
		Version:       version.String(),
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			path, err := cmd.Flags().GetString("config")
			if err != nil {
				return err
			}
			cfg, err := config.Load(path)
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}
			if err := cfg.EnsureDirs(); err != nil {
				return fmt.Errorf("preparing data directories: %w", err)
			}

			logger, closeFn, err := logging.New(logging.Options{
				Dir:    cfg.LogsDir(),
				Level:  cfg.App.LogLevel,
				Format: cfg.App.LogFormat,
			})
			if err != nil {
				return fmt.Errorf("initializing logging: %w", err)
			}
			closeLogging = closeFn

			if err := maildir.SweepAll(cfg.Storage.MaildirPath); err != nil {
				logger.Warn("maildir tmp/ sweep failed", zap.Error(err))
			}

			ctx := withConfig(cmd.Context(), cfg)
			ctx = withLogger(ctx, logger)
			cmd.SetContext(ctx)

			logger.Info("command started", zap.String("command", cmd.CommandPath()))
			return nil
		},
		PersistentPostRunE: func(cmd *cobra.Command, args []string) error {
			if closeLogging == nil {
				return nil
			}
			err := closeLogging()
			closeLogging = nil
			return err
		},
	}
	root.SetVersionTemplate("mailvault {{.Version}}\n")
	root.PersistentFlags().String("config", "./config.yaml", "path to config.yaml (missing file is not an error)")

	root.AddCommand(newConfigCmd())
	root.AddCommand(newUnlockCmd())
	root.AddCommand(newMigrateCmd())
	root.AddCommand(newAddAccountCmd())
	root.AddCommand(newListAccountsCmd())
	root.AddCommand(newTestConnectionCmd())
	root.AddCommand(newSyncCmd())
	root.AddCommand(newStatusCmd())

	return root
}
