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

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"

	"github.com/yurydemin/marchi/internal/config"
)

// New builds the Fiber app.
func New(logger *zap.Logger) *fiber.App {
	app := fiber.New(fiber.Config{
		AppName:               "MailVault",
		DisableStartupMessage: true,
	})

	app.Get("/", func(c *fiber.Ctx) error {
		return c.SendString("MailVault is running.")
	})

	return app
}

// Serve runs the HTTP(S) server until ctx is cancelled (NFR-RL-05), then
// shuts it down and returns ctx.Err() — mirroring how every other command
// reports a deliberate shutdown, so main.go's existing
// errors.Is(err, context.Canceled) handling applies uniformly here too.
//
// Shutdown itself has no separate timeout: it relies on main.go's existing
// 30-second force-exit watchdog, the same safety net the sync engine's own
// graceful shutdown (step 16, Phase 1) depends on.
func Serve(ctx context.Context, cfg *config.Config, logger *zap.Logger) error {
	app := New(logger)
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
		return ctx.Err()
	}
}
