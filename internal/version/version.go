// Package version holds build metadata injected via -ldflags at release build time.
package version

var (
	Version   = "dev"
	Commit    = "none"
	BuildDate = "unknown"
)

func String() string {
	return Version + " (commit " + Commit + ", built " + BuildDate + ")"
}
