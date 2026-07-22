// Command marchi is the single-binary entry point for the Marchi email archiving service.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"github.com/yurydemin/marchi/internal/config"
	"github.com/yurydemin/marchi/internal/httpapi"
	"github.com/yurydemin/marchi/internal/i18n"
	"github.com/yurydemin/marchi/internal/logging"
	"github.com/yurydemin/marchi/internal/maildir"
	"github.com/yurydemin/marchi/internal/version"
)

// gracefulShutdownTimeout is NFR-RL-05's 30 seconds: how long a command
// gets to wind down after SIGINT/SIGTERM before this process force-exits.
const gracefulShutdownTimeout = 30 * time.Second

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go forceExitOnTimeout(ctx, gracefulShutdownTimeout)

	err := newRootCmd(i18n.NewLocalizer(resolveCLILang(os.Args[1:]))).ExecuteContext(ctx)
	if err == nil {
		return
	}
	if errors.Is(err, context.Canceled) {
		// A deliberate, successful shutdown (SIGINT/SIGTERM), not a failure
		// — exit 0 rather than printing a scary "Error: context canceled".
		fmt.Fprintln(os.Stderr, "marchi: shutdown requested, exited cleanly")
		return
	}
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

// forceExitOnTimeout waits for a shutdown signal, then force-exits if the
// process hasn't wound down on its own within timeout (NFR-RL-05: "если не
// завершилось — force exit"). If the process exits normally beforehand,
// this goroutine is simply torn down with everything else — no explicit
// cleanup needed for a program that's already terminating.
func forceExitOnTimeout(ctx context.Context, timeout time.Duration) {
	<-ctx.Done()
	time.Sleep(timeout)
	fmt.Fprintln(os.Stderr, "marchi: graceful shutdown timed out, forcing exit")
	os.Exit(1)
}

// resolveCLILang picks the language cobra's Short/Use help text is built
// in — resolved once, before newRootCmd constructs the command tree,
// because those strings are baked in at construction time (see
// newRootCmd's own doc comment). Checked in the same precedence order as
// most CLI tools: an explicit --lang flag first, then LC_ALL/LANG
// (POSIX's own precedence between the two), then i18n.Default. This is a
// hand-rolled scan rather than a cobra flag lookup specifically because
// it has to run before any cobra.Command exists at all.
func resolveCLILang(args []string) string {
	for i, a := range args {
		if v, ok := strings.CutPrefix(a, "--lang="); ok {
			return v
		}
		if a == "--lang" && i+1 < len(args) {
			return args[i+1]
		}
	}
	for _, envVar := range []string{"LC_ALL", "LANG"} {
		v := os.Getenv(envVar)
		if v == "" {
			continue
		}
		if idx := strings.IndexAny(v, "_."); idx >= 0 {
			v = v[:idx]
		}
		return v
	}
	return i18n.Default
}

// newRootCmd builds the full command tree. loc is resolved once by
// main() (see resolveCLILang) and threaded into every subcommand
// constructor because cobra bakes each Command's Short/Use text in at
// construction time — unlike a flag's value, there's no later point
// where "just read loc again" would pick up a different language.
func newRootCmd(loc *i18n.Localizer) *cobra.Command {
	var closeLogging func() error

	root := &cobra.Command{
		Use:          "marchi",
		Short:        loc.T("cli.root.short"),
		SilenceUsage: true,
		// main() already prints every error itself (both the "clean
		// shutdown" and generic cases below) — leaving Cobra's own default
		// error printer enabled would double-print "Error: ..." for every
		// failing command.
		SilenceErrors: true,
		Version:       version.String(),
		// With no subcommand, marchi starts the web server (NFR-DP-02:
		// "zero-config запуск ... запускает веб-интерфейс").
		RunE: func(cmd *cobra.Command, args []string) error {
			return httpapi.Serve(cmd.Context(), configFrom(cmd.Context()), loggerFrom(cmd.Context()))
		},
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
	root.SetVersionTemplate("marchi {{.Version}}\n")
	root.PersistentFlags().String("config", "./config.yaml", "path to config.yaml (missing file is not an error)")
	root.PersistentFlags().String("lang", "", loc.T("cli.lang_flag_help"))

	root.AddCommand(newConfigCmd(loc))
	root.AddCommand(newUnlockCmd(loc))
	root.AddCommand(newMigrateCmd(loc))
	root.AddCommand(newAddAccountCmd(loc))
	root.AddCommand(newListAccountsCmd(loc))
	root.AddCommand(newTestConnectionCmd(loc))
	root.AddCommand(newSyncCmd(loc))
	root.AddCommand(newStatusCmd(loc))
	root.AddCommand(newLogsCmd(loc))
	root.AddCommand(newReindexCmd(loc))
	root.AddCommand(newRetentionCmd(loc))

	return root
}
