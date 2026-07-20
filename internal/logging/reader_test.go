package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeLogFile(t *testing.T, dir, date string, lines []string) {
	t.Helper()
	path := filepath.Join(dir, fmt.Sprintf("marchi-%s.log", date))
	content := strings.Join(lines, "\n")
	if len(lines) > 0 {
		content += "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestTailLines_FewerLinesThanRequested(t *testing.T) {
	dir := t.TempDir()
	writeLogFile(t, dir, "2026-07-17", []string{"line1", "line2", "line3"})

	got, err := TailLines(dir, "2026-07-17", 10)
	if err != nil {
		t.Fatalf("TailLines: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d lines, want 3: %v", len(got), got)
	}
	if got[0] != "line1" || got[2] != "line3" {
		t.Errorf("lines = %v", got)
	}
}

func TestTailLines_ExactlyN(t *testing.T) {
	dir := t.TempDir()
	var lines []string
	for i := 0; i < 20; i++ {
		lines = append(lines, fmt.Sprintf("line%d", i))
	}
	writeLogFile(t, dir, "2026-07-17", lines)

	got, err := TailLines(dir, "2026-07-17", 5)
	if err != nil {
		t.Fatalf("TailLines: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("got %d lines, want 5: %v", len(got), got)
	}
	want := []string{"line15", "line16", "line17", "line18", "line19"}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("got[%d] = %q, want %q", i, got[i], w)
		}
	}
}

func TestTailLines_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	writeLogFile(t, dir, "2026-07-17", nil)

	got, err := TailLines(dir, "2026-07-17", 10)
	if err != nil {
		t.Fatalf("TailLines: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d lines, want 0", len(got))
	}
}

func TestTailLines_NoFileForDate(t *testing.T) {
	dir := t.TempDir()
	_, err := TailLines(dir, "2026-01-01", 10)
	if err == nil {
		t.Fatal("expected an error for a missing log file, got nil")
	}
}

func TestTailLines_InvalidDate(t *testing.T) {
	dir := t.TempDir()
	_, err := TailLines(dir, "not-a-date", 10)
	if err == nil {
		t.Fatal("expected an error for an invalid date, got nil")
	}
}

// TestTailLines_AcrossMultipleChunks forces tailFile's backward-read loop
// to iterate more than once: each line is long enough that tailChunkSize
// (64KB) doesn't cover the requested tail in a single read.
func TestTailLines_AcrossMultipleChunks(t *testing.T) {
	dir := t.TempDir()
	longSuffix := strings.Repeat("x", 2000) // ~2KB per line
	var lines []string
	for i := 0; i < 100; i++ { // ~200KB total, well past one 64KB chunk
		lines = append(lines, fmt.Sprintf("line%d-%s", i, longSuffix))
	}
	writeLogFile(t, dir, "2026-07-17", lines)

	got, err := TailLines(dir, "2026-07-17", 10)
	if err != nil {
		t.Fatalf("TailLines: %v", err)
	}
	if len(got) != 10 {
		t.Fatalf("got %d lines, want 10", len(got))
	}
	for i, wantIdx := 0, 90; i < 10; i, wantIdx = i+1, wantIdx+1 {
		wantPrefix := fmt.Sprintf("line%d-", wantIdx)
		if !strings.HasPrefix(got[i], wantPrefix) {
			t.Errorf("got[%d] doesn't start with %q: %q...", i, wantPrefix, got[i][:20])
		}
	}
}

func TestTailLines_DefaultsToToday(t *testing.T) {
	dir := t.TempDir()
	today := time.Now().Format(dateLayout)
	writeLogFile(t, dir, today, []string{"todays line"})

	got, err := TailLines(dir, "", 10)
	if err != nil {
		t.Fatalf("TailLines: %v", err)
	}
	if len(got) != 1 || got[0] != "todays line" {
		t.Errorf("got %v", got)
	}
}
