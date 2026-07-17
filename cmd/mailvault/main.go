// Command mailvault is the single-binary entry point for the MailVault email archiving service.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/yurydemin/marchi/internal/version"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "mailvault",
		Short:         "MailVault — self-hosted email archiving service",
		SilenceUsage:  true,
		SilenceErrors: false,
		Version:       version.String(),
	}
	root.SetVersionTemplate("mailvault {{.Version}}\n")

	return root
}
