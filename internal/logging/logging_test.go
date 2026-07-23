package logging

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestNew_WritesJSONToRotatingFile(t *testing.T) {
	dir := t.TempDir()
	logger, closeFn, err := New(Options{Dir: dir, Level: "info", Format: "json"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	logger.Info("hello from test", zap.String("k", "v"))
	if err := closeFn(); err != nil {
		t.Fatalf("close: %v", err)
	}

	name := "marchi-" + time.Now().Format("2006-01-02") + ".log"
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("expected log file: %v", err)
	}
	if !strings.Contains(string(data), "hello from test") {
		t.Errorf("log content missing message: %s", data)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(data)), "{") {
		t.Errorf("expected JSON-shaped log line, got: %s", data)
	}
}

func TestNew_LevelFiltering(t *testing.T) {
	dir := t.TempDir()
	logger, closeFn, err := New(Options{Dir: dir, Level: "warn", Format: "json"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	logger.Info("should be filtered out")
	logger.Warn("should appear")
	if err := closeFn(); err != nil {
		t.Fatalf("close: %v", err)
	}

	name := "marchi-" + time.Now().Format("2006-01-02") + ".log"
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("expected log file: %v", err)
	}
	if strings.Contains(string(data), "should be filtered out") {
		t.Errorf("info line leaked through warn-level filter: %s", data)
	}
	if !strings.Contains(string(data), "should appear") {
		t.Errorf("warn line missing: %s", data)
	}
}

func TestNew_InvalidLevel(t *testing.T) {
	if _, _, err := New(Options{Dir: t.TempDir(), Level: "not-a-level"}); err == nil {
		t.Fatal("expected error for invalid level, got nil")
	}
}

func TestNew_DefaultOutput_WritesBothFileAndStderr(t *testing.T) {
	dir := t.TempDir()
	stderr := captureStderr(t)
	logger, closeFn, err := New(Options{Dir: dir, Level: "info", Format: "json"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	logger.Info("both-sinks line")
	if err := closeFn(); err != nil {
		t.Fatalf("close: %v", err)
	}

	name := "marchi-" + time.Now().Format("2006-01-02") + ".log"
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("expected log file (Output defaults to \"both\"): %v", err)
	}
	if !strings.Contains(string(data), "both-sinks line") {
		t.Errorf("file sink missing message: %s", data)
	}
	if !strings.Contains(stderr(), "both-sinks line") {
		t.Errorf("console sink missing message: %s", stderr())
	}
}

// TestNew_OutputStdout_WritesToStderrNotFile guards Options.Output's own
// doc comment: despite being named "stdout" (what operators search for),
// this sink is os.Stderr — see that comment for why.
func TestNew_OutputStdout_WritesToStderrNotFile(t *testing.T) {
	dir := t.TempDir()
	stderr := captureStderr(t)
	logger, closeFn, err := New(Options{Dir: dir, Level: "info", Format: "json", Output: OutputStdout})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	logger.Info("console-only line")
	if err := closeFn(); err != nil {
		t.Fatalf("close: %v", err)
	}

	name := "marchi-" + time.Now().Format("2006-01-02") + ".log"
	if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
		t.Errorf("Output: stdout must not create a log file, stat err = %v", err)
	}
	if !strings.Contains(stderr(), "console-only line") {
		t.Errorf("stderr missing message: %s", stderr())
	}
}

func TestNew_OutputFile_NothingWrittenToConsole(t *testing.T) {
	dir := t.TempDir()
	stderr := captureStderr(t)
	logger, closeFn, err := New(Options{Dir: dir, Level: "info", Format: "json", Output: OutputFile})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	logger.Info("file-only line")
	if err := closeFn(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if strings.Contains(stderr(), "file-only line") {
		t.Errorf("Output: file must not write to stderr, got: %s", stderr())
	}
	name := "marchi-" + time.Now().Format("2006-01-02") + ".log"
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("expected log file: %v", err)
	}
	if !strings.Contains(string(data), "file-only line") {
		t.Errorf("file sink missing message: %s", data)
	}
}

func TestNew_InvalidOutput(t *testing.T) {
	if _, _, err := New(Options{Dir: t.TempDir(), Output: "syslog"}); err == nil {
		t.Fatal("expected error for invalid Output, got nil")
	}
}

// captureStderr redirects os.Stderr to a pipe for the rest of the test,
// restoring it on cleanup, and returns a function that reads whatever was
// written so far.
func captureStderr(t *testing.T) func() string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stderr
	os.Stderr = w
	t.Cleanup(func() {
		os.Stderr = orig
	})
	return func() string {
		_ = w.Close()
		data, _ := io.ReadAll(r)
		return string(data)
	}
}

func TestNew_ConsoleFormat(t *testing.T) {
	dir := t.TempDir()
	logger, closeFn, err := New(Options{Dir: dir, Level: "info", Format: "console"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	logger.Info("console line")
	if err := closeFn(); err != nil {
		t.Fatalf("close: %v", err)
	}

	name := "marchi-" + time.Now().Format("2006-01-02") + ".log"
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("expected log file: %v", err)
	}
	if strings.HasPrefix(strings.TrimSpace(string(data)), "{") {
		t.Errorf("expected non-JSON console-shaped log line, got: %s", data)
	}
}
