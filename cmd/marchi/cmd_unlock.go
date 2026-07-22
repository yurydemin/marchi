package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/yurydemin/marchi/internal/i18n"
)

func newUnlockCmd(loc *i18n.Localizer) *cobra.Command {
	return &cobra.Command{
		Use:   "unlock",
		Short: loc.T("cli.unlock.short"),
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
