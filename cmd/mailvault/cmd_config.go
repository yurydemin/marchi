package main

import (
	"fmt"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/yurydemin/marchi/internal/config"
)

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Inspect the resolved configuration",
	}
	cmd.AddCommand(newConfigShowCmd())
	return cmd
}

func newConfigShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Print the fully resolved configuration (defaults + config.yaml + env overrides)",
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := cmd.Flags().GetString("config")
			if err != nil {
				return err
			}
			cfg, err := config.Load(path)
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}
			out, err := yaml.Marshal(cfg)
			if err != nil {
				return fmt.Errorf("marshaling config: %w", err)
			}
			fmt.Fprint(cmd.OutOrStdout(), string(out))
			return nil
		},
	}
}
