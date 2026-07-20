package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/yurydemin/marchi/internal/logging"
)

func newLogsCmd() *cobra.Command {
	var (
		lines int
		date  string
		level string
		raw   bool
	)

	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Show recent application log lines",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := configFrom(cmd.Context())

			var minSeverity int
			if level != "" {
				if _, ok := knownLevels[strings.ToLower(level)]; !ok {
					return fmt.Errorf("invalid --level %q (want debug, info, warn, or error)", level)
				}
				minSeverity = logging.LevelSeverity(level)
			}

			logLines, err := logging.TailLines(cfg.LogsDir(), date, lines)
			if err != nil {
				return err
			}

			for _, line := range logLines {
				if raw {
					fmt.Fprintln(cmd.OutOrStdout(), line)
					continue
				}

				entry, parseErr := logging.ParseLine(line)
				if parseErr != nil {
					fmt.Fprintln(cmd.OutOrStdout(), line) // fall back to the raw line rather than dropping it
					continue
				}
				if level != "" && logging.LevelSeverity(entry.Level) < minSeverity {
					continue
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s %-5s %s%s\n",
					entry.Timestamp, strings.ToUpper(entry.Level), entry.Message, formatLogFields(entry.Fields))
			}
			return nil
		},
	}

	cmd.Flags().IntVar(&lines, "lines", 50, "number of recent lines to show")
	cmd.Flags().StringVar(&date, "date", "", "log date to view, YYYY-MM-DD (default: today)")
	cmd.Flags().StringVar(&level, "level", "", "only show this level and above: debug, info, warn, error")
	cmd.Flags().BoolVar(&raw, "raw", false, "print raw JSON lines instead of formatted output")

	return cmd
}

var knownLevels = map[string]bool{"debug": true, "info": true, "warn": true, "error": true}

func formatLogFields(fields map[string]any) string {
	if len(fields) == 0 {
		return ""
	}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s=%v", k, fields[k])
	}
	return "  " + strings.Join(parts, " ")
}
