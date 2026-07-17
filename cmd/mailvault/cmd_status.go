package main

import (
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/yurydemin/marchi/internal/db"
	"github.com/yurydemin/marchi/internal/db/repo"
	"github.com/yurydemin/marchi/internal/domain"
)

func newStatusCmd() *cobra.Command {
	var limit int

	cmd := &cobra.Command{
		Use:   "status [email]",
		Short: "Show recent sync run history (sync_logs) — for one account, or across all of them",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := configFrom(cmd.Context())

			sqlDB, err := db.Open(cfg.Database.SQLite.Path)
			if err != nil {
				return err
			}
			defer sqlDB.Close()

			accountsRepo := repo.NewAccountsRepo(sqlDB, nil)
			syncLogsRepo := repo.NewSyncLogsRepo(sqlDB, nil)

			accounts, err := accountsRepo.List(cmd.Context())
			if err != nil {
				return err
			}
			emailByAccountID := make(map[int64]string, len(accounts))
			for _, a := range accounts {
				emailByAccountID[a.ID] = a.Email
			}

			var logs []*domain.SyncLog
			if len(args) == 1 {
				a, err := accountsRepo.GetByEmail(cmd.Context(), args[0])
				if err != nil {
					return fmt.Errorf("account %s not found: %w", args[0], err)
				}
				logs, err = syncLogsRepo.ListByAccount(cmd.Context(), a.ID, limit)
				if err != nil {
					return err
				}
			} else {
				logs, err = syncLogsRepo.ListRecent(cmd.Context(), limit)
				if err != nil {
					return err
				}
			}

			if len(logs) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No sync runs recorded yet.")
				return nil
			}

			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tACCOUNT\tSTARTED\tDURATION\tSTATUS\tPROCESSED\tARCHIVED\tERRORS")
			for _, l := range logs {
				fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%d\t%d\t%d\n",
					l.ID, emailByAccountID[l.AccountID], l.StartedAt.Format("2006-01-02 15:04:05"),
					formatDuration(l), l.Status, l.EmailsProcessed, l.EmailsArchived, l.Errors)
			}
			if err := tw.Flush(); err != nil {
				return err
			}

			// Error messages are printed as plain lines below the table,
			// not as tab-aligned cells — a long message would otherwise
			// stretch every other row's column widths too, since
			// text/tabwriter sizes columns across the whole block.
			for _, l := range logs {
				if l.ErrorMsg != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "  [%d] error: %s\n", l.ID, l.ErrorMsg)
				}
			}
			return nil
		},
	}

	cmd.Flags().IntVar(&limit, "limit", 20, "maximum number of runs to show")

	return cmd
}

func formatDuration(l *domain.SyncLog) string {
	if l.Status == domain.SyncLogRunning || l.EndedAt.IsZero() {
		return "running"
	}
	return l.EndedAt.Sub(l.StartedAt).Round(time.Second).String()
}
