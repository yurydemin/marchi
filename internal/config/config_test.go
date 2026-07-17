package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_ZeroConfig_NoFile(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.App.DataDir != "./data" {
		t.Errorf("DataDir = %q, want ./data", cfg.App.DataDir)
	}
	if cfg.HTTP.Host != "127.0.0.1" || cfg.HTTP.Port != 8080 {
		t.Errorf("HTTP = %s:%d, want 127.0.0.1:8080", cfg.HTTP.Host, cfg.HTTP.Port)
	}
	if cfg.Database.SQLite.Path != filepath.Join("./data", "mailvault.db") {
		t.Errorf("SQLite path = %q", cfg.Database.SQLite.Path)
	}
}

func TestLoad_YAMLOverridesDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	yamlContent := `
app:
  data_dir: "/custom/data"
  log_level: "debug"
http:
  port: 9090
sync:
  max_concurrent_accounts: 2
`
	if err := os.WriteFile(path, []byte(yamlContent), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.App.DataDir != "/custom/data" {
		t.Errorf("DataDir = %q, want /custom/data", cfg.App.DataDir)
	}
	if cfg.App.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want debug", cfg.App.LogLevel)
	}
	if cfg.HTTP.Port != 9090 {
		t.Errorf("Port = %d, want 9090", cfg.HTTP.Port)
	}
	// Untouched by YAML, must retain default.
	if cfg.HTTP.Host != "127.0.0.1" {
		t.Errorf("Host = %q, want default 127.0.0.1 to survive partial override", cfg.HTTP.Host)
	}
	if cfg.Sync.MaxConcurrentAccounts != 2 {
		t.Errorf("MaxConcurrentAccounts = %d, want 2", cfg.Sync.MaxConcurrentAccounts)
	}
	if cfg.Sync.DefaultSchedule != "0 */6 * * *" {
		t.Errorf("DefaultSchedule = %q, want default to survive", cfg.Sync.DefaultSchedule)
	}
}

func TestLoad_EnvOverridesYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	yamlContent := `
app:
  data_dir: "/from/yaml"
  log_level: "debug"
http:
  port: 9090
`
	if err := os.WriteFile(path, []byte(yamlContent), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("MAILVAULT_DATA_DIR", "/from/env")
	t.Setenv("MAILVAULT_HTTP_PORT", "7070")
	t.Setenv("MAILVAULT_LOG_LEVEL", "warn")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.App.DataDir != "/from/env" {
		t.Errorf("DataDir = %q, want env override /from/env", cfg.App.DataDir)
	}
	if cfg.HTTP.Port != 7070 {
		t.Errorf("Port = %d, want env override 7070", cfg.HTTP.Port)
	}
	if cfg.App.LogLevel != "warn" {
		t.Errorf("LogLevel = %q, want env override warn", cfg.App.LogLevel)
	}
}

func TestLoad_InvalidPortEnv(t *testing.T) {
	t.Setenv("MAILVAULT_HTTP_PORT", "not-a-number")
	if _, err := Load(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Fatal("expected error for invalid MAILVAULT_HTTP_PORT, got nil")
	}
}

func TestLoad_ValidatesPortRange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("http:\n  port: 70000\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected validation error for out-of-range port, got nil")
	}
}

func TestEnsureDirs(t *testing.T) {
	base := t.TempDir()
	dataDir := filepath.Join(base, "data")
	cfg := Defaults(dataDir)

	if err := cfg.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}

	for _, d := range []string{dataDir, cfg.Search.IndexPath, cfg.Storage.MaildirPath, cfg.Storage.Cache.Path, cfg.LogsDir()} {
		info, err := os.Stat(d)
		if err != nil {
			t.Errorf("expected dir %s to exist: %v", d, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("%s is not a directory", d)
		}
	}
}
