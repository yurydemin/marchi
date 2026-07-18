// Package httpapi is MailVault's HTTP server (FR-WU-*, FR-API-*): the
// long-running Fiber process that, unlike Phase 1's one-shot CLI commands,
// stays up across many requests and starts in a locked state until the
// Master Key is supplied (see the unlock-gate added in a later step).
//
// This step only stands up the server process itself — self-signed TLS,
// listen/shutdown wired into the same graceful-shutdown context every CLI
// command already uses. Routes beyond a placeholder root handler arrive in
// later steps.
package httpapi

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/csrf"
	"github.com/gofiber/fiber/v2/middleware/limiter"
	"github.com/gofiber/fiber/v2/middleware/recover"
	"go.uber.org/zap"

	"github.com/yurydemin/marchi/internal/config"
	"github.com/yurydemin/marchi/internal/security/masterkey"
)

// globalRateLimit is NFR-SC-07's "1000 req/min for everything else".
const globalRateLimit = 1000

// New builds the Fiber app: recover/rate-limit/CSRF middleware, the
// locked-state gate, and the /unlock endpoint (поправки #2/#3,
// NFR-SC-06/07). If MAILVAULT_MASTER_KEY is set, the vault is unlocked
// process-wide immediately — matching how every Phase 1 CLI command
// already treats that env var (NFR-SC-01) — though, per unlock.go's doc
// comment, that alone doesn't authenticate any browser session; a fresh
// browser still has to hit /unlock once to get its own session cookie.
func New(cfg *config.Config, logger *zap.Logger) (*fiber.App, *vaultState) {
	app := fiber.New(fiber.Config{
		AppName:               "MailVault",
		DisableStartupMessage: true,
	})

	hub := newWSHub()
	vault := newVaultState(func(key []byte) (*backend, error) {
		return newBackend(cfg, logger, key, hub)
	})
	unlockFromEnv(cfg, logger, vault)

	store := newSessionStore(cfg)

	app.Use(recover.New())
	app.Use(limiter.New(limiter.Config{
		Max:        globalRateLimit,
		Expiration: time.Minute,
	}))
	app.Use(csrf.New(csrf.Config{
		Session:        store,
		CookieSecure:   cfg.HTTP.TLS.Enabled,
		CookieSameSite: "Lax",
	}))
	app.Use(newLockGate(store))

	registerUnlock(app, cfg, logger, vault, store)
	registerSearch(app, vault)
	registerAccounts(app, vault)
	registerEmails(app, vault)
	registerStats(app, vault)
	registerLogs(app, vault)
	registerAdmin(app, vault)
	registerWS(app, hub)

	app.Get("/", func(c *fiber.Ctx) error {
		return c.SendString("MailVault is running.")
	})

	return app, vault
}

// unlockFromEnv mirrors cmd/mailvault's own unlockMasterKey for the
// env-var path (NFR-SC-01): if MAILVAULT_MASTER_KEY is set, unlock the
// vault right away instead of waiting for an interactive web /unlock.
// Unlike the CLI, there's no interactive stdin fallback here — a server
// with no env var and no browser unlock yet just stays locked.
func unlockFromEnv(cfg *config.Config, logger *zap.Logger, vault *vaultState) {
	envPassword, ok := os.LookupEnv(cfg.Security.MasterKeyEnv)
	if !ok || envPassword == "" {
		return
	}
	logger.Warn("SECURITY WARNING: master key password supplied via environment variable; only use this for unattended/systemd startup",
		zap.String("env_var", cfg.Security.MasterKeyEnv))

	params := masterkey.Argon2Params{
		Memory:      cfg.Security.Argon2.Memory,
		Iterations:  cfg.Security.Argon2.Iterations,
		Parallelism: cfg.Security.Argon2.Parallelism,
	}
	key, err := masterkey.Unlock(envPassword,
		masterkey.SaltPath(cfg.App.DataDir), masterkey.VerifyPath(cfg.App.DataDir), params)
	if err != nil {
		logger.Error("startup vault unlock via environment variable failed; vault remains locked", zap.Error(err))
		return
	}
	if _, err := vault.unlock(key); err != nil {
		logger.Error("startup vault unlock via environment variable failed while initializing backend; vault remains locked", zap.Error(err))
		return
	}
	logger.Info("vault unlocked at startup", zap.String("source", "env"))
}

// Serve runs the HTTP(S) server until ctx is cancelled (NFR-RL-05), then
// shuts it down and returns ctx.Err() — mirroring how every other command
// reports a deliberate shutdown, so main.go's existing
// errors.Is(err, context.Canceled) handling applies uniformly here too.
//
// Shutdown itself has no separate timeout on the HTTP layer: it relies on
// main.go's existing 30-second force-exit watchdog, the same safety net
// the sync engine's own graceful shutdown (step 16, Phase 1) depends on.
// The backend (scheduler, database), if the vault was ever unlocked, gets
// its own bounded drain (scheduler.shutdownDrainTimeout) inside that
// window before the database is closed.
func Serve(ctx context.Context, cfg *config.Config, logger *zap.Logger) error {
	app, vault := New(cfg, logger)
	addr := fmt.Sprintf("%s:%d", cfg.HTTP.Host, cfg.HTTP.Port)

	serveErr := make(chan error, 1)
	go func() {
		if !cfg.HTTP.TLS.Enabled {
			logger.Info("http server listening", zap.String("addr", addr), zap.Bool("tls", false))
			serveErr <- app.Listen(addr)
			return
		}

		certFile, keyFile, err := EnsureTLSCert(cfg)
		if err != nil {
			serveErr <- fmt.Errorf("httpapi: preparing TLS certificate: %w", err)
			return
		}
		logger.Info("http server listening", zap.String("addr", addr), zap.Bool("tls", true), zap.String("cert", certFile))
		serveErr <- app.ListenTLS(addr, certFile, keyFile)
	}()

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
		logger.Info("http server shutting down")
		if err := app.Shutdown(); err != nil {
			return fmt.Errorf("httpapi: shutdown: %w", err)
		}
		if b := vault.currentBackend(); b != nil {
			b.close(logger)
		}
		return ctx.Err()
	}
}
