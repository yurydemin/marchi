// Package logging wires uber-go/zap to a daily-rotating file sink per
// NFR-RL-04: {data_dir}/logs/mailvault-{YYYY-MM-DD}.log, 30-day retention,
// 100MB max file size.
package logging

import (
	"fmt"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

const (
	// DefaultMaxSizeMB and DefaultMaxAgeDays match NFR-RL-04 exactly.
	DefaultMaxSizeMB  = 100
	DefaultMaxAgeDays = 30

	filePrefix = "mailvault"
)

// Options configures New. Zero values fall back to NFR-RL-04 defaults.
type Options struct {
	// Dir is the log directory, normally Config.LogsDir().
	Dir string
	// Level is a zap level string: debug, info, warn, error.
	Level string
	// Format is "json" (default) or "console".
	Format string
	// MaxSizeMB caps a single day's log file before it spills into a
	// same-day overflow part. 0 uses DefaultMaxSizeMB.
	MaxSizeMB int
	// MaxAgeDays is how long rotated files are kept. 0 uses DefaultMaxAgeDays.
	MaxAgeDays int
}

// New builds a *zap.Logger writing exclusively to the rotating file sink,
// and returns a close func to flush/release the underlying file on shutdown.
func New(opts Options) (*zap.Logger, func() error, error) {
	if opts.Dir == "" {
		return nil, nil, fmt.Errorf("logging: Dir must not be empty")
	}
	maxSizeMB := opts.MaxSizeMB
	if maxSizeMB <= 0 {
		maxSizeMB = DefaultMaxSizeMB
	}
	maxAgeDays := opts.MaxAgeDays
	if maxAgeDays <= 0 {
		maxAgeDays = DefaultMaxAgeDays
	}

	var level zapcore.Level
	if opts.Level == "" {
		level = zapcore.InfoLevel
	} else if err := level.UnmarshalText([]byte(opts.Level)); err != nil {
		return nil, nil, fmt.Errorf("logging: invalid level %q: %w", opts.Level, err)
	}

	writer, err := newDailyRotatingWriter(opts.Dir, filePrefix, int64(maxSizeMB)*1024*1024, maxAgeDays, nil)
	if err != nil {
		return nil, nil, err
	}

	encoder := jsonEncoder()
	if opts.Format == "console" {
		encoder = consoleEncoder()
	}

	core := zapcore.NewCore(encoder, zapcore.AddSync(writer), level)
	logger := zap.New(core)

	closeFn := func() error {
		_ = logger.Sync()
		return writer.Close()
	}
	return logger, closeFn, nil
}

func jsonEncoder() zapcore.Encoder {
	cfg := zap.NewProductionEncoderConfig()
	cfg.TimeKey = "ts"
	cfg.EncodeTime = zapcore.ISO8601TimeEncoder
	return zapcore.NewJSONEncoder(cfg)
}

func consoleEncoder() zapcore.Encoder {
	cfg := zap.NewDevelopmentEncoderConfig()
	cfg.EncodeTime = zapcore.ISO8601TimeEncoder
	return zapcore.NewConsoleEncoder(cfg)
}
