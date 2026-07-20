package main

import (
	"context"

	"go.uber.org/zap"

	"github.com/yurydemin/marchi/internal/config"
)

type ctxKey int

const (
	ctxKeyConfig ctxKey = iota
	ctxKeyLogger
)

func withConfig(ctx context.Context, cfg *config.Config) context.Context {
	return context.WithValue(ctx, ctxKeyConfig, cfg)
}

func configFrom(ctx context.Context) *config.Config {
	cfg, _ := ctx.Value(ctxKeyConfig).(*config.Config)
	return cfg
}

func withLogger(ctx context.Context, l *zap.Logger) context.Context {
	return context.WithValue(ctx, ctxKeyLogger, l)
}

func loggerFrom(ctx context.Context) *zap.Logger {
	if l, ok := ctx.Value(ctxKeyLogger).(*zap.Logger); ok && l != nil {
		return l
	}
	return zap.NewNop()
}
