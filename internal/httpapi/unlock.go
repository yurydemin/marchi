package httpapi

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/limiter"
	"github.com/gofiber/fiber/v2/middleware/session"
	"go.uber.org/zap"

	"github.com/yurydemin/marchi/internal/config"
	"github.com/yurydemin/marchi/internal/security/masterkey"
)

// sessionUnlockedKey is the session data key marking a browser as
// authenticated (поправка #2: "Master Key = логин в Web UI"). Deliberately
// distinct from vaultState: even when the vault is already unlocked
// process-wide via MARCHI_MASTER_KEY at startup, a fresh browser still
// has no session and must submit the password once via POST /unlock to
// get one — the process already holding the key doesn't grant network
// access to it for free.
const sessionUnlockedKey = "unlocked"

// unlockRateLimit is NFR-SC-07's "100 req/min for auth endpoints".
const unlockRateLimit = 100

type unlockRequest struct {
	Password string `json:"password" form:"password"`
}

// registerUnlock wires POST /unlock: verifies the submitted password
// against the vault (bootstrapping it on first run, exactly like the CLI's
// own unlock flow), marks the vault unlocked process-wide, and grants the
// requesting browser its own session.
func registerUnlock(app *fiber.App, cfg *config.Config, logger *zap.Logger, vault *vaultState, store *session.Store) {
	app.Post("/unlock", limiter.New(limiter.Config{
		Max:        unlockRateLimit,
		Expiration: time.Minute,
	}), func(c *fiber.Ctx) error {
		var req unlockRequest
		if err := c.BodyParser(&req); err != nil {
			return fiber.NewError(fiber.StatusBadRequest, "invalid request body")
		}

		params := masterkey.Argon2Params{
			Memory:      cfg.Security.Argon2.Memory,
			Iterations:  cfg.Security.Argon2.Iterations,
			Parallelism: cfg.Security.Argon2.Parallelism,
		}
		dek, err := masterkey.UnlockDEK(req.Password,
			masterkey.SaltPath(cfg.App.DataDir), masterkey.VerifyPath(cfg.App.DataDir), masterkey.DEKPath(cfg.App.DataDir), params)
		switch {
		case err == nil:
			// fall through
		case errors.Is(err, masterkey.ErrPasswordTooShort):
			return fiber.NewError(fiber.StatusBadRequest, err.Error())
		default:
			logger.Warn("web unlock attempt failed", zap.Error(err), zap.String("ip", c.IP()))
			return fiber.NewError(fiber.StatusUnauthorized, "incorrect password")
		}

		if _, err := vault.unlock(dek); err != nil {
			logger.Error("unlocking vault failed", zap.Error(err))
			return fiber.NewError(fiber.StatusInternalServerError, "unlock failed")
		}

		sess, err := store.Get(c)
		if err != nil {
			return fmt.Errorf("httpapi: loading session: %w", err)
		}
		sess.Set(sessionUnlockedKey, true)
		if err := sess.Save(); err != nil {
			return fmt.Errorf("httpapi: saving session: %w", err)
		}

		logger.Info("web session unlocked", zap.String("ip", c.IP()))
		return c.JSON(fiber.Map{"status": "unlocked"})
	})
}

// sessionUnlocked reports whether the requesting browser's own session is
// authenticated — distinct from the vault being unlocked process-wide
// (see sessionUnlockedKey's doc comment). Page handlers (pages.go) use
// this to decide which content to render; a false here means "show the
// unlock form", not "error".
func sessionUnlocked(c *fiber.Ctx, store *session.Store) bool {
	sess, err := store.Get(c)
	if err != nil {
		return false
	}
	unlocked, _ := sess.Get(sessionUnlockedKey).(bool)
	return unlocked
}

// newLockGate blocks the JSON API and WebSocket until the requesting
// browser's own session is authenticated (поправка #3: locked-state — only
// the unlock endpoint is reachable pre-unlock). Server-rendered pages and
// static assets are deliberately not gated here: the unlock page itself
// needs its CSS/JS to load pre-unlock, and page handlers render a real
// (locked or unlocked) page rather than a bare 401 — see pages.go.
func newLockGate(store *session.Store) fiber.Handler {
	return func(c *fiber.Ctx) error {
		path := c.Path()
		if !strings.HasPrefix(path, "/api/v1") && path != "/ws" {
			return c.Next()
		}
		if !sessionUnlocked(c, store) {
			return fiber.NewError(fiber.StatusUnauthorized, "vault is locked")
		}
		return c.Next()
	}
}
