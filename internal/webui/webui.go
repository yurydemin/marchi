// Package webui parses Marchi's embedded html/template sources and
// exposes its embedded static assets (Tailwind CSS, self-hosted HTMX).
// The templates and assets themselves live in the repo-root web/ package
// (see its doc comment for why); this package is where they're compiled
// into something internal/httpapi can render and serve.
package webui

import (
	"fmt"
	"html/template"
	"io/fs"

	"github.com/yurydemin/marchi/web"
)

// pageFiles maps a route's page name to its content template file. Each
// page gets its own *template.Template built from layout.html plus
// exactly one page file, because html/template shares a single
// {{define "content"}} namespace across every file parsed together — if
// two page files were parsed into the same template set, the second
// definition would silently win for both.
var pageFiles = map[string]string{
	"index":    "templates/index.html",
	"accounts": "templates/accounts.html",
	"archive":  "templates/archive.html",
	"rules":    "templates/rules.html",
	"settings": "templates/settings.html",
}

// Parse compiles every registered page once. Called at server startup
// (internal/httpapi.New), not per-request.
func Parse() (map[string]*template.Template, error) {
	pages := make(map[string]*template.Template, len(pageFiles))
	for name, file := range pageFiles {
		t, err := template.New("layout.html").Funcs(funcs).ParseFS(web.Templates, "templates/layout.html", file)
		if err != nil {
			return nil, fmt.Errorf("webui: parsing page %q: %w", name, err)
		}
		pages[name] = t
	}
	return pages, nil
}

// StaticFS returns the embedded CSS/JS assets, rooted so files are served
// at the paths the templates reference (/static/css/app.css, etc).
func StaticFS() fs.FS {
	sub, err := fs.Sub(web.Static, "static")
	if err != nil {
		// Unreachable: "static" is a directory embedded by web.Static's own
		// go:embed directive, so fs.Sub can only fail here if that build
		// tag were ever removed.
		panic("webui: static assets not embedded: " + err.Error())
	}
	return sub
}
