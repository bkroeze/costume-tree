// Package config loads process settings from the environment.
// COSTUME_TREE_ADDR defaults to :8080, COSTUME_TREE_DB_PATH defaults to
// /data/costume-tree.db, COSTUME_TREE_SHUTDOWN_TIMEOUT defaults to 10s,
// COSTUME_TREE_REQUEST_TIMEOUT defaults to 30s, and
// COSTUME_TREE_MAX_BODY_BYTES defaults to 1 MiB.
package config

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	defaultAddress         = ":8080"
	defaultDatabasePath    = "/data/costume-tree.db"
	defaultShutdownTimeout = 10 * time.Second
	defaultRequestTimeout  = 30 * time.Second
	defaultMaxBodyBytes    = 1 << 20
	maxConfiguredBodyBytes = 64 << 20
)

// Settings contains configuration required by the application shell.
type Settings struct {
	Address         string
	DatabasePath    string
	ShutdownTimeout time.Duration
	RequestTimeout  time.Duration
	MaxBodyBytes    int64
}

// Load reads and validates settings through lookup.
func Load(lookup func(string) (string, bool)) (Settings, error) {
	settings := Settings{
		Address:         defaultAddress,
		DatabasePath:    defaultDatabasePath,
		ShutdownTimeout: defaultShutdownTimeout,
		RequestTimeout:  defaultRequestTimeout,
		MaxBodyBytes:    defaultMaxBodyBytes,
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
		timeout, err := parsePositiveDuration("COSTUME_TREE_SHUTDOWN_TIMEOUT", value)
		if err != nil {
			return Settings{}, err
		}
		settings.ShutdownTimeout = timeout
	}
	if value, ok := lookup("COSTUME_TREE_REQUEST_TIMEOUT"); ok {
		timeout, err := parsePositiveDuration("COSTUME_TREE_REQUEST_TIMEOUT", value)
		if err != nil {
			return Settings{}, err
		}
		settings.RequestTimeout = timeout
	}
	if value, ok := lookup("COSTUME_TREE_MAX_BODY_BYTES"); ok {
		bytes, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return Settings{}, fmt.Errorf("config: parse COSTUME_TREE_MAX_BODY_BYTES: %w", err)
		}
		if bytes <= 0 || bytes > maxConfiguredBodyBytes {
			return Settings{}, fmt.Errorf("config: COSTUME_TREE_MAX_BODY_BYTES must be between 1 and %d", maxConfiguredBodyBytes)
		}
		settings.MaxBodyBytes = bytes
	}

	return settings, nil
}

func parsePositiveDuration(name, value string) (time.Duration, error) {
	timeout, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("config: parse %s: %w", name, err)
	}
	if timeout <= 0 {
		return 0, fmt.Errorf("config: %s must be positive", name)
	}
	return timeout, nil
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
