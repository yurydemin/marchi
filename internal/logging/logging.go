// Package logging wires uber-go/zap to a daily-rotating file sink per
// NFR-RL-04: {data_dir}/logs/marchi-{YYYY-MM-DD}.log, 30-day retention,
// 100MB max file size — and, by default, a second console sink alongside
// it (see Options.Output's doc comment for why both are on by default,
// and why that sink is stderr despite being called "stdout").
package logging

import (
	"fmt"
	"os"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

const (
	// DefaultMaxSizeMB and DefaultMaxAgeDays match NFR-RL-04 exactly.
	DefaultMaxSizeMB  = 100
	DefaultMaxAgeDays = 30

	filePrefix = "marchi"

	// OutputFile and OutputStdout are Options.Output's two sinks;
	// OutputBoth (the default — see Options.Output) writes to both.
	OutputFile   = "file"
	OutputStdout = "stdout"
	OutputBoth   = "both"
)

// Options configures New. Zero values fall back to NFR-RL-04 defaults.
type Options struct {
	// Dir is the log directory, normally Config.LogsDir(). Required even
	// when Output is OutputStdout-only, to keep this package's one entry
	// point simple — callers always have a data dir on hand regardless of
	// which sink they end up using.
	Dir string
	// Level is a zap level string: debug, info, warn, error.
	Level string
	// Format is "json" (default) or "console". Applies to both sinks —
	// this package doesn't support a different format per sink.
	Format string
	// Output selects which sink(s) receive log lines: OutputFile,
	// OutputStdout, or OutputBoth. Empty defaults to OutputBoth: the
	// console sink is what makes `docker logs`/`journalctl -u marchi`
	// show anything without extra configuration (both capture a
	// process's stderr right alongside its stdout, no Type=notify or
	// syslog integration needed), while the file sink is what
	// `marchi logs` (internal/logging.TailLines) reads — keeping both on
	// by default means neither workflow needs an opt-in.
	//
	// OutputStdout writes to os.Stderr, not os.Stdout, despite the name:
	// the name matches what operators actually search for ("get my logs
	// into docker logs"), but the CLI's own one-shot commands write their
	// real output to stdout (marchi config show's YAML, marchi
	// list-accounts' table, ...) — logging there too would corrupt
	// anything piped or redirected from stdout. stderr gets identical
	// visibility in both docker logs and journalctl without that
	// footgun.
	Output string
	// MaxSizeMB caps a single day's log file before it spills into a
	// same-day overflow part. 0 uses DefaultMaxSizeMB. Ignored if Output
	// is OutputStdout-only.
	MaxSizeMB int
	// MaxAgeDays is how long rotated files are kept. 0 uses DefaultMaxAgeDays.
	// Ignored if Output is OutputStdout-only.
	MaxAgeDays int
}

// New builds a *zap.Logger writing to the sink(s) Options.Output selects,
// and returns a close func to flush/release any underlying file on
// shutdown (a no-op when Output is OutputStdout-only).
func New(opts Options) (*zap.Logger, func() error, error) {
	if opts.Dir == "" {
		return nil, nil, fmt.Errorf("logging: Dir must not be empty")
	}
	output := opts.Output
	if output == "" {
		output = OutputBoth
	}
	if output != OutputFile && output != OutputStdout && output != OutputBoth {
		return nil, nil, fmt.Errorf("logging: invalid Output %q (want %q, %q, or %q)", output, OutputFile, OutputStdout, OutputBoth)
	}

	var level zapcore.Level
	if opts.Level == "" {
		level = zapcore.InfoLevel
	} else if err := level.UnmarshalText([]byte(opts.Level)); err != nil {
		return nil, nil, fmt.Errorf("logging: invalid level %q: %w", opts.Level, err)
	}

	encoder := jsonEncoder()
	if opts.Format == "console" {
		encoder = consoleEncoder()
	}

	var cores []zapcore.Core
	var writer *dailyRotatingWriter

	if output == OutputFile || output == OutputBoth {
		maxSizeMB := opts.MaxSizeMB
		if maxSizeMB <= 0 {
			maxSizeMB = DefaultMaxSizeMB
		}
		maxAgeDays := opts.MaxAgeDays
		if maxAgeDays <= 0 {
			maxAgeDays = DefaultMaxAgeDays
		}
		var err error
		writer, err = newDailyRotatingWriter(opts.Dir, filePrefix, int64(maxSizeMB)*1024*1024, maxAgeDays, nil)
		if err != nil {
			return nil, nil, err
		}
		cores = append(cores, zapcore.NewCore(encoder, zapcore.AddSync(writer), level))
	}
	if output == OutputStdout || output == OutputBoth {
		cores = append(cores, zapcore.NewCore(encoder, zapcore.Lock(zapcore.AddSync(os.Stderr)), level))
	}

	logger := zap.New(zapcore.NewTee(cores...))

	closeFn := func() error {
		_ = logger.Sync()
		if writer == nil {
			return nil
		}
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
