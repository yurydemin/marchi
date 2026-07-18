// Package config loads MailVault's configuration from config.yaml, layering
// environment variable overrides on top per NFR-DP-03 (env wins over YAML).
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"gopkg.in/yaml.v3"
)

type Config struct {
	App      AppConfig      `yaml:"app"`
	HTTP     HTTPConfig     `yaml:"http"`
	Security SecurityConfig `yaml:"security"`
	Database DatabaseConfig `yaml:"database"`
	Search   SearchConfig   `yaml:"search"`
	Storage  StorageConfig  `yaml:"storage"`
	S3       S3Config       `yaml:"s3"`
	Sync     SyncConfig     `yaml:"sync"`
}

type AppConfig struct {
	DataDir   string `yaml:"data_dir"`
	LogLevel  string `yaml:"log_level"`
	LogFormat string `yaml:"log_format"`
}

type HTTPConfig struct {
	Host string    `yaml:"host"`
	Port int       `yaml:"port"`
	TLS  TLSConfig `yaml:"tls"`
}

type TLSConfig struct {
	Enabled  bool   `yaml:"enabled"`
	AutoCert bool   `yaml:"auto_cert"`
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
}

type SecurityConfig struct {
	MasterKeyEnv string       `yaml:"master_key_env"`
	Argon2       Argon2Config `yaml:"argon2"`
}

// Argon2Config mirrors the tuning parameters from tech-spec §8.1. Memory is in KiB.
type Argon2Config struct {
	Memory      uint32 `yaml:"memory"`
	Iterations  uint32 `yaml:"iterations"`
	Parallelism uint8  `yaml:"parallelism"`
}

type DatabaseConfig struct {
	Type   string       `yaml:"type"`
	SQLite SQLiteConfig `yaml:"sqlite"`
}

type SQLiteConfig struct {
	Path string `yaml:"path"`
}

type SearchConfig struct {
	IndexPath string `yaml:"index_path"`
}

type StorageConfig struct {
	MaildirPath string      `yaml:"maildir_path"`
	Cache       CacheConfig `yaml:"cache"`
}

type CacheConfig struct {
	Enabled   bool   `yaml:"enabled"`
	MaxSizeGB int    `yaml:"max_size_gb"`
	Path      string `yaml:"path"`
}

// S3Config is deliberately just operational tuning, not connection
// details: endpoint/region/bucket/credentials are per-deployment secrets
// edited live through the Settings UI/API (FR-WU-02, Phase 3 step 9) and
// therefore live in the s3_config DB table (domain.S3Settings), encrypted
// under the Master Key like every other credential in this app — not in
// this plaintext, restart-only config file. Rewriting config.yaml from a
// running API handler was considered and rejected: gopkg.in/yaml.v3
// doesn't preserve comments/formatting on marshal, so every API-driven
// edit would silently reformat (or outright clobber) whatever the user
// originally wrote.
type S3Config struct {
	Encryption    S3EncryptionConfig `yaml:"encryption"`
	UploadWorkers int                `yaml:"upload_workers"`
}

type S3EncryptionConfig struct {
	Enabled   bool   `yaml:"enabled"`
	Algorithm string `yaml:"algorithm"`
}

type SyncConfig struct {
	DefaultSchedule       string `yaml:"default_schedule"`
	MaxConcurrentAccounts int    `yaml:"max_concurrent_accounts"`
}

// Defaults returns the zero-config baseline (NFR-DP-02): everything rooted
// under dataDir, HTTP bound to localhost only until the user opts into wider
// exposure via config.yaml or env vars.
func Defaults(dataDir string) *Config {
	return &Config{
		App: AppConfig{
			DataDir:   dataDir,
			LogLevel:  "info",
			LogFormat: "json",
		},
		HTTP: HTTPConfig{
			Host: "127.0.0.1",
			Port: 8080,
			TLS: TLSConfig{
				Enabled:  true,
				AutoCert: true,
			},
		},
		Security: SecurityConfig{
			MasterKeyEnv: "MAILVAULT_MASTER_KEY",
			Argon2: Argon2Config{
				Memory:      65536,
				Iterations:  3,
				Parallelism: 4,
			},
		},
		Database: DatabaseConfig{
			Type: "sqlite",
			SQLite: SQLiteConfig{
				Path: filepath.Join(dataDir, "mailvault.db"),
			},
		},
		Search: SearchConfig{
			IndexPath: filepath.Join(dataDir, "index"),
		},
		Storage: StorageConfig{
			MaildirPath: filepath.Join(dataDir, "maildir"),
			Cache: CacheConfig{
				Enabled:   true,
				MaxSizeGB: 10,
				Path:      filepath.Join(dataDir, "cache"),
			},
		},
		S3: S3Config{
			Encryption: S3EncryptionConfig{
				Enabled:   true,
				Algorithm: "AES-256-GCM",
			},
			UploadWorkers: 4,
		},
		Sync: SyncConfig{
			DefaultSchedule:       "0 */6 * * *",
			MaxConcurrentAccounts: 5,
		},
	}
}

// envOverrides lists the env vars NFR-DP-03 calls out explicitly, and how
// each one maps onto the resolved Config. Env vars always win over config.yaml.
var envOverrides = []struct {
	name  string
	apply func(*Config, string) error
}{
	{"MAILVAULT_DATA_DIR", func(c *Config, v string) error {
		c.App.DataDir = v
		return nil
	}},
	{"MAILVAULT_HTTP_PORT", func(c *Config, v string) error {
		port, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("MAILVAULT_HTTP_PORT: invalid port %q: %w", v, err)
		}
		c.HTTP.Port = port
		return nil
	}},
	{"MAILVAULT_LOG_LEVEL", func(c *Config, v string) error {
		c.App.LogLevel = v
		return nil
	}},
}

// Load reads config.yaml at path (if present — its absence is not an error,
// zero-config just runs on defaults), then applies env var overrides on top.
//
// data_dir is resolved in a first pass (YAML, then MAILVAULT_DATA_DIR)
// before Defaults is called, so Database.SQLite.Path/Search.IndexPath/
// Storage.MaildirPath/Storage.Cache.Path — all derived from dataDir —
// come out rooted at the user's chosen directory even when the config
// only sets app.data_dir and leaves those four unspecified. Building cfg
// from Defaults("./data") and unmarshaling data_dir on top afterward (the
// previous approach) left those four permanently pointing at ./data
// regardless of data_dir, since nothing ever re-derived them.
func Load(path string) (*Config, error) {
	dataDir := "./data"
	var fileBytes []byte

	if path != "" {
		data, err := os.ReadFile(path)
		switch {
		case err == nil:
			fileBytes = data
			var probe struct {
				App struct {
					DataDir string `yaml:"data_dir"`
				} `yaml:"app"`
			}
			if err := yaml.Unmarshal(data, &probe); err != nil {
				return nil, fmt.Errorf("parsing %s: %w", path, err)
			}
			if probe.App.DataDir != "" {
				dataDir = probe.App.DataDir
			}
		case os.IsNotExist(err):
			// zero-config: no file, defaults stand.
		default:
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}
	}
	if v, ok := os.LookupEnv("MAILVAULT_DATA_DIR"); ok {
		dataDir = v
	}

	cfg := Defaults(dataDir)
	if fileBytes != nil {
		if err := yaml.Unmarshal(fileBytes, cfg); err != nil {
			return nil, fmt.Errorf("parsing %s: %w", path, err)
		}
	}

	for _, o := range envOverrides {
		if v, ok := os.LookupEnv(o.name); ok {
			if err := o.apply(cfg, v); err != nil {
				return nil, err
			}
		}
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

func (c *Config) validate() error {
	if c.App.DataDir == "" {
		return fmt.Errorf("app.data_dir must not be empty")
	}
	if c.HTTP.Port < 1 || c.HTTP.Port > 65535 {
		return fmt.Errorf("http.port must be between 1 and 65535, got %d", c.HTTP.Port)
	}
	return nil
}

// EnsureDirs creates the directories this config points at (NFR-DP-02's
// zero-config startup behavior). Safe to call every startup.
func (c *Config) EnsureDirs() error {
	dirs := []string{
		c.App.DataDir,
		filepath.Dir(c.Database.SQLite.Path),
		c.Search.IndexPath,
		c.Storage.MaildirPath,
		c.LogsDir(),
	}
	if c.Storage.Cache.Enabled {
		dirs = append(dirs, c.Storage.Cache.Path)
	}
	if c.HTTP.TLS.Enabled && c.HTTP.TLS.AutoCert {
		dirs = append(dirs, c.TLSDir())
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return fmt.Errorf("creating directory %s: %w", d, err)
		}
	}
	return nil
}

// LogsDir is always {data_dir}/logs — not independently configurable (NFR-RL-04).
func (c *Config) LogsDir() string {
	return filepath.Join(c.App.DataDir, "logs")
}

// TLSDir is where an auto-generated self-signed certificate (NFR-SC-04) is
// stored and reused across restarts — not independently configurable, same
// as LogsDir.
func (c *Config) TLSDir() string {
	return filepath.Join(c.App.DataDir, "tls")
}
