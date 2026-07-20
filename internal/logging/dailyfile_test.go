package logging

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func mustReadDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestDailyRotatingWriter_CreatesTodayFile(t *testing.T) {
	dir := t.TempDir()
	clock := func() time.Time { return time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC) }

	w, err := newDailyRotatingWriter(dir, "marchi", 0, 0, clock)
	if err != nil {
		t.Fatalf("newDailyRotatingWriter: %v", err)
	}
	defer w.Close()

	if _, err := w.Write([]byte("hello\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	want := "marchi-2026-07-17.log"
	data, err := os.ReadFile(filepath.Join(dir, want))
	if err != nil {
		t.Fatalf("expected %s to exist: %v", want, err)
	}
	if string(data) != "hello\n" {
		t.Errorf("file content = %q, want %q", data, "hello\n")
	}
}

func TestDailyRotatingWriter_DayRollover(t *testing.T) {
	dir := t.TempDir()
	day := time.Date(2026, 7, 17, 23, 0, 0, 0, time.UTC)
	clock := func() time.Time { return day }

	w, err := newDailyRotatingWriter(dir, "marchi", 0, 0, clock)
	if err != nil {
		t.Fatalf("newDailyRotatingWriter: %v", err)
	}
	defer w.Close()

	if _, err := w.Write([]byte("day1\n")); err != nil {
		t.Fatal(err)
	}

	day = time.Date(2026, 7, 18, 0, 5, 0, 0, time.UTC)
	if _, err := w.Write([]byte("day2\n")); err != nil {
		t.Fatal(err)
	}

	names := mustReadDir(t, dir)
	wantFiles := map[string]bool{"marchi-2026-07-17.log": true, "marchi-2026-07-18.log": true}
	for _, n := range names {
		delete(wantFiles, n)
	}
	if len(wantFiles) != 0 {
		t.Errorf("missing expected files, got dir listing %v", names)
	}
}

func TestDailyRotatingWriter_SizeOverflowSameDay(t *testing.T) {
	dir := t.TempDir()
	clock := func() time.Time { return time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC) }

	// 10-byte cap forces overflow after the first write.
	w, err := newDailyRotatingWriter(dir, "marchi", 10, 0, clock)
	if err != nil {
		t.Fatalf("newDailyRotatingWriter: %v", err)
	}
	defer w.Close()

	if _, err := w.Write([]byte("0123456789")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("overflow!!")); err != nil {
		t.Fatal(err)
	}

	names := mustReadDir(t, dir)
	found2 := false
	for _, n := range names {
		if n == "marchi-2026-07-17.2.log" {
			found2 = true
		}
	}
	if !found2 {
		t.Errorf("expected an overflow part file marchi-2026-07-17.2.log, got %v", names)
	}
}

func TestDailyRotatingWriter_RetentionSweep(t *testing.T) {
	dir := t.TempDir()
	today := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)
	clock := func() time.Time { return today }

	// Pre-seed an old file (40 days back, beyond a 30-day retention) and a
	// recent one (5 days back), plus a file with a different prefix that
	// must never be touched.
	old := filepath.Join(dir, "marchi-2026-06-07.log")
	recent := filepath.Join(dir, "marchi-2026-07-12.log")
	other := filepath.Join(dir, "otherapp-2026-01-01.log")
	for _, p := range []string{old, recent, other} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	w, err := newDailyRotatingWriter(dir, "marchi", 0, 30, clock)
	if err != nil {
		t.Fatalf("newDailyRotatingWriter: %v", err)
	}
	defer w.Close()

	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("expected old file to be swept, stat err = %v", err)
	}
	if _, err := os.Stat(recent); err != nil {
		t.Errorf("expected recent file to survive: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("expected other-prefix file to survive untouched: %v", err)
	}
}
