package logging

import (
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

	name := "mailvault-" + time.Now().Format("2006-01-02") + ".log"
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

	name := "mailvault-" + time.Now().Format("2006-01-02") + ".log"
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

	name := "mailvault-" + time.Now().Format("2006-01-02") + ".log"
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("expected log file: %v", err)
	}
	if strings.HasPrefix(strings.TrimSpace(string(data)), "{") {
		t.Errorf("expected non-JSON console-shaped log line, got: %s", data)
	}
}
