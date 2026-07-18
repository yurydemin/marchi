package rules

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"
)

// waitForRuleCount polls rulesRepo.List until it has exactly n rows or
// timeout elapses, since WatchYAML's reload is async (debounced fsnotify).
func waitForRuleCount(t *testing.T, repoList func() int, n int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if repoList() == n {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d rule(s), still have %d", n, repoList())
}

func TestWatchYAML_LoadsExistingFileOnStartup(t *testing.T) {
	rulesRepo := openTestRulesRepo(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "rules.yaml")
	if err := os.WriteFile(path, []byte(validYAML), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go WatchYAML(ctx, path, rulesRepo, zap.NewNop())

	waitForRuleCount(t, func() int {
		rules, _ := rulesRepo.List(context.Background())
		return len(rules)
	}, 2, 2*time.Second)
}

func TestWatchYAML_MissingFileAtStartupIsFine(t *testing.T) {
	rulesRepo := openTestRulesRepo(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "rules.yaml") // never created

	ctx, cancel := context.WithCancel(context.Background())
	go WatchYAML(ctx, path, rulesRepo, zap.NewNop())
	time.Sleep(200 * time.Millisecond) // let the initial (no-op) load run
	cancel()

	rules, err := rulesRepo.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rules) != 0 {
		t.Errorf("got %d rules from a nonexistent file, want 0", len(rules))
	}
}

func TestWatchYAML_ReloadsOnFileChange(t *testing.T) {
	rulesRepo := openTestRulesRepo(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "rules.yaml")
	// Start with the file absent — WatchYAML must pick it up once created,
	// not just watch for changes to an already-existing file.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go WatchYAML(ctx, path, rulesRepo, zap.NewNop())

	time.Sleep(100 * time.Millisecond) // let the watcher's initial (no-op) load and Add() finish

	if err := os.WriteFile(path, []byte(validYAML), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	waitForRuleCount(t, func() int {
		rules, _ := rulesRepo.List(context.Background())
		return len(rules)
	}, 2, 3*time.Second)
}

func TestWatchYAML_StopsOnContextCancel(t *testing.T) {
	rulesRepo := openTestRulesRepo(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "rules.yaml")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		WatchYAML(ctx, path, rulesRepo, zap.NewNop())
		close(done)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("WatchYAML did not return after its context was cancelled")
	}
}
