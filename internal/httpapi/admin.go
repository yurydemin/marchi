package httpapi

import "github.com/gofiber/fiber/v2"

// registerAdmin wires POST /api/v1/admin/reindex (FR-SR-04), the web
// counterpart of the CLI's `mailvault reindex`. Unlike the CLI version,
// this one runs against a live server — see backend.runReindex's doc
// comment for how that's kept safe without pausing the Scheduler.
//
// Synchronous for now, same as the manual sync trigger (Phase 2 step 7):
// FR-SR-04 calls for WebSocket progress, added in a later step, which can
// move this to the background.
func registerAdmin(app *fiber.App, vault *vaultState) {
	app.Post("/api/v1/admin/reindex", handleAdminReindex(vault))
}

func handleAdminReindex(vault *vaultState) fiber.Handler {
	return func(c *fiber.Ctx) error {
		b, err := currentBackendOrLocked(vault)
		if err != nil {
			return err
		}

		stats, err := b.runReindex(c.Context())
		if err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, "reindex failed: "+err.Error())
		}
		return c.JSON(fiber.Map{
			"total":   stats.Total,
			"indexed": stats.Indexed,
			"skipped": stats.Skipped,
			"errors":  stats.Errors,
		})
	}
}
