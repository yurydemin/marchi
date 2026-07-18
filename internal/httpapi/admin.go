package httpapi

import (
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// registerAdmin wires POST /api/v1/admin/reindex (FR-SR-04), the web
// counterpart of the CLI's `mailvault reindex`. Unlike the CLI version,
// this one runs against a live server — see backend.runReindex's doc
// comment for how that's kept safe without pausing the Scheduler.
//
// Returns immediately with a job id (FR-SR-04's "WebSocket-прогресс"):
// the actual rebuild runs in a tracked background goroutine
// (backend.runReindexAsync), broadcasting progress and completion over
// /ws under that id.
func registerAdmin(app *fiber.App, vault *vaultState) {
	app.Post("/api/v1/admin/reindex", handleAdminReindex(vault))
}

func handleAdminReindex(vault *vaultState) fiber.Handler {
	return func(c *fiber.Ctx) error {
		b, err := currentBackendOrLocked(vault)
		if err != nil {
			return err
		}

		jobID := uuid.NewString()
		b.runReindexAsync(jobID)
		return c.Status(fiber.StatusAccepted).JSON(fiber.Map{"job_id": jobID})
	}
}
