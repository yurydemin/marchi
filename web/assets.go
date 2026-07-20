// Package web embeds Marchi's html/template sources, Tailwind-built
// CSS, and self-hosted HTMX so the server ships as a single binary with
// no runtime dependency on Node.js, a CDN, or a filesystem checkout
// (NFR-DP-02). Templates and CSS are authored here; internal/webui parses
// and serves them.
package web

import "embed"

//go:embed templates/*.html
var Templates embed.FS

//go:embed static
var Static embed.FS
