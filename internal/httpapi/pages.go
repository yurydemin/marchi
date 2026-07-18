package httpapi

import (
	"html/template"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/session"
)

// pageData is what every page template is rendered with. Unlocked drives
// the locked/unlocked branch inside index.html's content block — page
// routes render a real page either way rather than a bare 401, unlike the
// JSON API's lock-gate (see newLockGate's doc comment). Stats is only
// populated (and only read by the template) when Unlocked is true.
//
// Unlocked reflects the browser's own session (sessionUnlocked), not
// whether the vault is unlocked process-wide: with MAILVAULT_MASTER_KEY
// set, the backend exists from startup, but a browser that never POSTed
// /unlock still has no session and must see the unlock form, exactly like
// the JSON API.
type pageData struct {
	Unlocked bool
	Stats    statsResponse
}

// registerPages wires the server-rendered html/template routes (FR-WU-*).
// "/" doubles as the unlock screen and the Dashboard (FR-WU-02) depending
// on session state; later steps add the accounts/archive pages the same
// way, each as its own entry in webui.Parse's page map.
func registerPages(app *fiber.App, vault *vaultState, store *session.Store, pages map[string]*template.Template) {
	app.Get("/", func(c *fiber.Ctx) error {
		c.Set(fiber.HeaderContentType, fiber.MIMETextHTMLCharsetUTF8)
		if !sessionUnlocked(c, store) {
			return pages["index"].ExecuteTemplate(c, "layout", pageData{Unlocked: false})
		}

		b := vault.currentBackend()
		if b == nil {
			// Shouldn't happen: sessionUnlocked implies a prior POST
			// /unlock already built the backend via vault.unlock().
			return pages["index"].ExecuteTemplate(c, "layout", pageData{Unlocked: false})
		}
		stats, err := computeStats(c.Context(), b)
		if err != nil {
			return err
		}
		return pages["index"].ExecuteTemplate(c, "layout", pageData{Unlocked: true, Stats: stats})
	})
}
