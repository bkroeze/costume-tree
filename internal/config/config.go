// Package config loads process settings from the environment.
// COSTUME_TREE_ADDR defaults to :8080 and COSTUME_TREE_SHUTDOWN_TIMEOUT
// defaults to 10s. Feature-specific configuration belongs with its owning
// feature and is assembled by the command composition root.
package config

import (
	"fmt"
	"time"
)

const (
	defaultAddress         = ":8080"
	defaultShutdownTimeout = 10 * time.Second
)

// Settings contains configuration required by the current application shell.
type Settings struct {
	Address         string
	ShutdownTimeout time.Duration
}

// Load reads and validates settings through lookup.
func Load(lookup func(string) (string, bool)) (Settings, error) {
	settings := Settings{
		Address:         defaultAddress,
		ShutdownTimeout: defaultShutdownTimeout,
	}

	if value, ok := lookup("COSTUME_TREE_ADDR"); ok {
		if value == "" {
			return Settings{}, fmt.Errorf("config: COSTUME_TREE_ADDR must not be empty")
		}
		settings.Address = value
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
