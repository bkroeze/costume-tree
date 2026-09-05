// Package config loads process settings from the environment.
// COSTUME_TREE_ADDR defaults to :8080, COSTUME_TREE_DB_PATH defaults to
// /data/costume-tree.db, and COSTUME_TREE_SHUTDOWN_TIMEOUT defaults to 10s.
package config

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultAddress         = ":8080"
	defaultDatabasePath    = "/data/costume-tree.db"
	defaultShutdownTimeout = 10 * time.Second
)

// Settings contains configuration required by the application shell.
type Settings struct {
	Address         string
	DatabasePath    string
	ShutdownTimeout time.Duration
}

// Load reads and validates settings through lookup.
func Load(lookup func(string) (string, bool)) (Settings, error) {
	settings := Settings{
		Address:         defaultAddress,
		DatabasePath:    defaultDatabasePath,
		ShutdownTimeout: defaultShutdownTimeout,
	}

	if value, ok := lookup("COSTUME_TREE_ADDR"); ok {
		if value == "" {
			return Settings{}, fmt.Errorf("config: COSTUME_TREE_ADDR must not be empty")
		}
		settings.Address = value
	}

	if value, ok := lookup("COSTUME_TREE_DB_PATH"); ok {
		settings.DatabasePath = value
	}
	if err := validateDatabasePath(settings.DatabasePath); err != nil {
		return Settings{}, err
	}

	if value, ok := lookup("COSTUME_TREE_SHUTDOWN_TIMEOUT"); ok {
		timeout, err := time.ParseDuration(value)
		if err != nil {
			return Settings{}, fmt.Errorf("config: parse COSTUME_TREE_SHUTDOWN_TIMEOUT: %w", err)
		}
		if timeout <= 0 {
			return Settings{}, fmt.Errorf("config: COSTUME_TREE_SHUTDOWN_TIMEOUT must be positive")
		}
		settings.ShutdownTimeout = timeout
	}

	return settings, nil
}

func validateDatabasePath(path string) error {
	if path == "" {
		return fmt.Errorf("config: COSTUME_TREE_DB_PATH must not be empty")
	}
	clean := filepath.Clean(path)
	if !filepath.IsAbs(path) || (clean != "/data" && !strings.HasPrefix(clean, "/data/")) {
		return fmt.Errorf("config: COSTUME_TREE_DB_PATH must be under /data")
	}
	if clean == "/data" {
		return fmt.Errorf("config: COSTUME_TREE_DB_PATH must name a file under /data")
	}
	return nil
}
