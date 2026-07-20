package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/yurydemin/marchi/internal/domain"
	"github.com/yurydemin/marchi/internal/imapclient"
)

func newTestConnectionCmd() *cobra.Command {
	var (
		imapHost     string
		imapPort     int
		imapTLS      string
		imapUsername string
	)

	cmd := &cobra.Command{
		Use:   "test-connection <email>",
		Short: "Test IMAP connectivity and credentials without saving an account (FR-AM-04)",
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
			username := imapUsername
			if username == "" {
				username = email
			}
			port := imapPort
			if port == 0 {
				port = defaultPortForTLS(tlsMode)
			}

			password, err := stdinSecrets.Read("IMAP password: ")
			if err != nil {
				return err
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), imapclient.DefaultDialTimeout)
			defer cancel()

			c, err := imapclient.Connect(ctx, imapclient.ConnectOptions{
				Host:     imapHost,
				Port:     port,
				TLS:      tlsMode,
				Username: username,
				Password: password,
			})
			if err != nil {
				// ConnectError's own Error() already distinguishes dial vs
				// TLS vs login failures (FR-AM-04) — nothing to add here.
				return err
			}
			defer c.Logout()

			folders, err := imapclient.ListFolders(c)
			if err != nil {
				return fmt.Errorf("connected and logged in, but listing folders failed: %w", err)
			}

			names := make([]string, len(folders))
			for i, f := range folders {
				names[i] = f.Name
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Connection OK. %d folder(s): %s\n", len(folders), strings.Join(names, ", "))
			return nil
		},
	}

	cmd.Flags().StringVar(&imapHost, "host", "", "IMAP server hostname (required)")
	cmd.Flags().IntVar(&imapPort, "port", 0, "IMAP port (default: 993 for ssl, 143 otherwise)")
	cmd.Flags().StringVar(&imapTLS, "tls", "ssl", "none, ssl, or starttls")
	cmd.Flags().StringVar(&imapUsername, "username", "", "IMAP login username (default: email)")

	return cmd
}

func defaultPortForTLS(tls domain.IMAPTLSMode) int {
	if tls == domain.IMAPTLSSSL {
		return 993
	}
	return 143
}
