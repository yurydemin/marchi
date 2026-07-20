package main

import (
	"fmt"
	"os"

	"go.uber.org/zap"

	"github.com/yurydemin/marchi/internal/config"
	"github.com/yurydemin/marchi/internal/security/masterkey"
)

// unlockMasterKey implements FR-ST-04 / NFR-SC-01's source priority:
// MARCHI_MASTER_KEY env var first (unattended startup, with the
// mandated SECURITY WARNING logged), otherwise an interactive prompt —
// which asks twice to set a brand new password on first run, once to
// unlock an existing vault otherwise.
func unlockMasterKey(cfg *config.Config, logger *zap.Logger) ([]byte, error) {
	saltPath := masterkey.SaltPath(cfg.App.DataDir)
	verifyPath := masterkey.VerifyPath(cfg.App.DataDir)
	params := masterkey.Argon2Params{
		Memory:      cfg.Security.Argon2.Memory,
		Iterations:  cfg.Security.Argon2.Iterations,
		Parallelism: cfg.Security.Argon2.Parallelism,
	}

	firstRun := masterkey.IsFirstRun(saltPath)

	if envPassword, ok := os.LookupEnv(cfg.Security.MasterKeyEnv); ok && envPassword != "" {
		logger.Warn("SECURITY WARNING: master key password supplied via environment variable; only use this for unattended/systemd startup",
			zap.String("env_var", cfg.Security.MasterKeyEnv))
		key, err := masterkey.Unlock(envPassword, saltPath, verifyPath, params)
		if err != nil {
			return nil, err
		}
		logger.Info("master key unlocked", zap.String("source", "env"), zap.Bool("first_run", firstRun))
		return key, nil
	}

	if firstRun {
		fmt.Fprintln(os.Stdout, "No Master Key found — set a new password (minimum 12 characters).")
	}
	password, err := masterkey.PromptPassword(stdinSecrets, firstRun)
	if err != nil {
		return nil, err
	}
	key, err := masterkey.Unlock(password, saltPath, verifyPath, params)
	if err != nil {
		return nil, err
	}
	logger.Info("master key unlocked", zap.String("source", "interactive"), zap.Bool("first_run", firstRun))
	return key, nil
}
