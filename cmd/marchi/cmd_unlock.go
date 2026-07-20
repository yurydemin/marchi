package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newUnlockCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unlock",
		Short: "Unlock the Master Key (or set one, on first run)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := configFrom(cmd.Context())
			logger := loggerFrom(cmd.Context())

			if _, err := unlockMasterKey(cfg, logger); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Unlocked.")
			return nil
		},
	}
}
