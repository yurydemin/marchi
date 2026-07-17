// Package migrations embeds the SQLite schema migrations into the binary
// (FR-DP-01: single binary, no external files needed at runtime).
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
