package httpapi

import "github.com/gofiber/fiber/v2"

// registerHealthz wires GET /healthz: always 200 as long as the process is
// alive and answering HTTP requests, regardless of vault lock state. This
// is deliberately different from every other status signal this project
// exposes — /metrics' marchi_unlocked gauge and the Dashboard both answer
// "is the archive usable", but a Docker/Kubernetes healthcheck needs to
// answer a narrower question first: "is the process itself still up, or
// does it need to be restarted?" A locked (not-yet-unlocked, or
// deliberately re-locked) vault is normal operation, not a failure a
// container orchestrator should react to by killing and restarting the
// process — that would just discard the in-memory unlock state for no
// benefit, forcing a human to re-enter the Master Key again.
//
// Registered at a bare path (not under /api/v1), so newLockGate's prefix
// check never applies here — an orchestrator's healthcheck has no browser
// session to unlock with, and shouldn't need one just to confirm the
// process is alive.
func registerHealthz(app *fiber.App) {
	app.Get("/healthz", func(c *fiber.Ctx) error {
		return c.SendStatus(fiber.StatusOK)
	})
}
