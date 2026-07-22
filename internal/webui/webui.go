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

	"github.com/yurydemin/marchi/internal/domain"
	"github.com/yurydemin/marchi/internal/i18n"
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

// Bind returns a copy of t (a page from Parse's result) with "T" and
// "conditionTypeLabel" rebound to loc, ready to Execute for one request.
//
// Templates are parsed once at startup (Parse), but the language a page
// renders in is only known per-request. html/template resolves a func
// call's target *value* at Execute time even though the call itself was
// validated at Parse time — Clone gives each request its own template
// handle to rebind those two names against without racing every other
// concurrent request's Execute against the same shared *template.Template
// (see https://pkg.go.dev/text/template#Template.Clone: "A common use is
// to prepare... variants" of a common parsed template).
func Bind(t *template.Template, loc *i18n.Localizer) (*template.Template, error) {
	ct, err := t.Clone()
	if err != nil {
		return nil, fmt.Errorf("webui: cloning template for locale %q: %w", loc.Lang, err)
	}
	return ct.Funcs(template.FuncMap{
		"T":                  loc.T,
		"conditionTypeLabel": func(t domain.ConditionType) string { return conditionTypeLabel(loc, t) },
	}), nil
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
