package main

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/yurydemin/marchi/internal/account"
	"github.com/yurydemin/marchi/internal/db"
	"github.com/yurydemin/marchi/internal/db/repo"
	"github.com/yurydemin/marchi/internal/db/writer"
	"github.com/yurydemin/marchi/internal/domain"
)

func newAddAccountCmd() *cobra.Command {
	var (
		displayName  string
		imapHost     string
		imapPort     int
		imapTLS      string
		imapUsername string
	)

	cmd := &cobra.Command{
		Use:   "add-account <email>",
		Short: "Add an IMAP account (FR-AM-01)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			email := args[0]
			if imapHost == "" {
				return fmt.Errorf("--host is required")
			}
			tlsMode, err := parseTLSMode(imapTLS)
			if err != nil {
				return err
			}

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

			mgr, err := account.NewManager(repo.NewAccountsRepo(sqlDB, w), masterKey)
			if err != nil {
				return err
			}

			password, err := stdinSecrets.Read("IMAP password: ")
			if err != nil {
				return err
			}

			a, err := mgr.AddAccount(cmd.Context(), account.AddAccountParams{
				Email:        email,
				DisplayName:  displayName,
				IMAPHost:     imapHost,
				IMAPPort:     imapPort,
				IMAPTLS:      tlsMode,
				IMAPUsername: imapUsername,
				IMAPPassword: password,
			})
			if err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Account added: id=%d email=%s host=%s:%d tls=%s\n",
				a.ID, a.Email, a.IMAPHost, a.IMAPPort, a.IMAPTLS)
			return nil
		},
	}

	cmd.Flags().StringVar(&displayName, "display-name", "", "friendly name shown in the UI")
	cmd.Flags().StringVar(&imapHost, "host", "", "IMAP server hostname (required)")
	cmd.Flags().IntVar(&imapPort, "port", 0, "IMAP port (default: 993 for ssl, 143 otherwise)")
	cmd.Flags().StringVar(&imapTLS, "tls", "ssl", "none, ssl, or starttls")
	cmd.Flags().StringVar(&imapUsername, "username", "", "IMAP login username (default: email)")

	return cmd
}

func newListAccountsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list-accounts",
		Short: "List configured IMAP accounts",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := configFrom(cmd.Context())
			logger := loggerFrom(cmd.Context())

			sqlDB, err := db.Open(cfg.Database.SQLite.Path)
			if err != nil {
				return err
			}
			defer closeDB(logger, sqlDB)

			accounts, err := repo.NewAccountsRepo(sqlDB, nil).List(cmd.Context())
			if err != nil {
				return err
			}
			if len(accounts) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No accounts configured.")
				return nil
			}

			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tEMAIL\tDISPLAY NAME\tHOST\tPORT\tTLS\tACTIVE")
			for _, a := range accounts {
				fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%d\t%s\t%s\n",
					a.ID, a.Email, a.DisplayName, a.IMAPHost, a.IMAPPort, a.IMAPTLS, yesNo(a.IsActive))
			}
			return tw.Flush()
		},
	}
}

func parseTLSMode(s string) (domain.IMAPTLSMode, error) {
	return domain.ParseIMAPTLSMode(s)
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
