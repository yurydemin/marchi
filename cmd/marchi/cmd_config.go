package main

import (
	"fmt"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/yurydemin/marchi/internal/i18n"
)

func newConfigCmd(loc *i18n.Localizer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: loc.T("cli.config.short"),
	}
	cmd.AddCommand(newConfigShowCmd(loc))
	return cmd
}

func newConfigShowCmd(loc *i18n.Localizer) *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: loc.T("cli.config_show.short"),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := configFrom(cmd.Context())
			out, err := yaml.Marshal(cfg)
			if err != nil {
				return fmt.Errorf("marshaling config: %w", err)
			}
			loggerFrom(cmd.Context()).Info("config show requested")
			fmt.Fprint(cmd.OutOrStdout(), string(out))
			return nil
		},
	}
}
