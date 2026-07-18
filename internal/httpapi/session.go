package httpapi

import (
	"time"

	"github.com/gofiber/fiber/v2/middleware/session"

	"github.com/yurydemin/marchi/internal/config"
)

// sessionExpiration isn't spec-mandated; 24h is a reasonable default for a
// single-tenant admin session. Not exposed as a config knob — nothing in
// the tech-spec calls for tuning it, and it's cheap to add later if that
// changes.
const sessionExpiration = 24 * time.Hour

// newSessionStore builds the session store backing both the unlock-gate
// and CSRF token issuance. In-memory (Fiber's default storage) and
// process-local is deliberate — poправка #2 — a session never survives a
// restart, matching the Master Key itself never being persisted anywhere
// either.
func newSessionStore(cfg *config.Config) *session.Store {
	return session.New(session.Config{
		Expiration:     sessionExpiration,
		CookieHTTPOnly: true,
		CookieSecure:   cfg.HTTP.TLS.Enabled,
		CookieSameSite: "Lax",
	})
}
