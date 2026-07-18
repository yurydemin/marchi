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
		b, ok := pageBackend(c, vault, store)
		if !ok {
			return renderLocked(c, pages)
		}
		stats, err := computeStats(c.Context(), b)
		if err != nil {
			return err
		}
		return pages["index"].ExecuteTemplate(c, "layout", pageData{Unlocked: true, Stats: stats})
	})
}

// renderLocked renders the shared unlock screen ("index" page's locked
// branch) for any full-page GET whose session isn't authenticated —
// browser navigation should land on something a user can act on, not a
// bare error.
func renderLocked(c *fiber.Ctx, pages map[string]*template.Template) error {
	c.Set(fiber.HeaderContentType, fiber.MIMETextHTMLCharsetUTF8)
	return pages["index"].ExecuteTemplate(c, "layout", pageData{Unlocked: false})
}

// pageBackend is the full-page-GET counterpart of requireUnlockedSession:
// same two checks (session authenticated, backend built), but reports
// failure via a bool instead of an error, since callers render a locked
// page rather than propagate a JSON-shaped error.
func pageBackend(c *fiber.Ctx, vault *vaultState, store *session.Store) (*backend, bool) {
	if !sessionUnlocked(c, store) {
		return nil, false
	}
	b := vault.currentBackend()
	// b == nil shouldn't happen: sessionUnlocked implies a prior POST
	// /unlock already built the backend via vault.unlock().
	return b, b != nil
}

// requireUnlockedSession is the HTMX-fragment counterpart of
// currentBackendOrLocked. Fragment routes (accounts_ui.go's htmx-driven
// create/edit/delete/toggle/test endpoints) sit outside newLockGate's
// scope — that gate only covers /api/v1 and /ws (see its doc comment) —
// so each of them must check the browser's own session itself. Without
// this, an unauthenticated request could mutate a vault some other
// session (or MAILVAULT_MASTER_KEY) already unlocked, defeating the
// per-browser-session auth model entirely.
func requireUnlockedSession(c *fiber.Ctx, vault *vaultState, store *session.Store) (*backend, error) {
	if !sessionUnlocked(c, store) {
		return nil, fiber.NewError(fiber.StatusUnauthorized, "vault is locked")
	}
	return currentBackendOrLocked(vault)
}
