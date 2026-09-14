// Package config loads process settings from the environment.
package config

import (
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strconv"
	"time"
)

const (
	defaultAddress         = ":8080"
	defaultPGPort          = 5432
	defaultPGDatabase      = "costume_tree"
	defaultPGSSLMode       = "prefer"
	defaultCostumeTreeDir  = "."
	defaultShutdownTimeout = 10 * time.Second
	defaultRequestTimeout  = 30 * time.Second
	defaultMaxBodyBytes    = 24 << 20
	maxConfiguredBodyBytes = 64 << 20
)

// Settings contains configuration required by the application shell.
type Settings struct {
	Address         string
	PGHost          string
	PGPort          uint16
	PGUser          string
	PGPassword      string
	PGDatabase      string
	PGSSLMode       string
	CostumeTreeDir  string
	ShutdownTimeout time.Duration
	RequestTimeout  time.Duration
	MaxBodyBytes    int64
}

// PostgresURL returns the PostgreSQL connection URL without exposing it through
// logging or other side effects.
func (settings Settings) PostgresURL() string {
	databasePath := "/" + settings.PGDatabase
	return (&url.URL{
		Scheme:  "postgres",
		User:    url.UserPassword(settings.PGUser, settings.PGPassword),
		Host:    net.JoinHostPort(settings.PGHost, strconv.FormatUint(uint64(settings.PGPort), 10)),
		Path:    databasePath,
		RawPath: "/" + url.PathEscape(settings.PGDatabase),
		RawQuery: url.Values{
			"search_path": []string{"public"},
			"sslmode":     []string{settings.PGSSLMode},
		}.Encode(),
	}).String()
}

// Load reads and validates settings through lookup.
func Load(lookup func(string) (string, bool)) (Settings, error) {
	settings := Settings{
		Address:         defaultAddress,
		PGPort:          defaultPGPort,
		PGDatabase:      defaultPGDatabase,
		PGSSLMode:       defaultPGSSLMode,
		CostumeTreeDir:  defaultCostumeTreeDir,
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

	for _, required := range []struct {
		name        string
		destination *string
	}{
		{name: "PG_HOST", destination: &settings.PGHost},
		{name: "PG_USER", destination: &settings.PGUser},
		{name: "PG_PASSWORD", destination: &settings.PGPassword},
	} {
		value, ok := lookup(required.name)
		if !ok || value == "" {
			return Settings{}, fmt.Errorf("config: %s is required and must not be empty", required.name)
		}
		*required.destination = value
	}

	if value, ok := lookup("PG_PORT"); ok {
		port, err := strconv.ParseUint(value, 10, 16)
		if err != nil || port == 0 {
			return Settings{}, fmt.Errorf("config: PG_PORT must be an integer between 1 and 65535")
		}
		settings.PGPort = uint16(port)
	}
	if value, ok := lookup("PG_DATABASE"); ok {
		if value == "" {
			return Settings{}, fmt.Errorf("config: PG_DATABASE must not be empty")
		}
		settings.PGDatabase = value
	}
	if value, ok := lookup("PG_SSLMODE"); ok {
		if value == "" {
			return Settings{}, fmt.Errorf("config: PG_SSLMODE must not be empty")
		}
		settings.PGSSLMode = value
	}

	if value, ok := lookup("COSTUMETREE_DIR"); ok {
		if value == "" {
			return Settings{}, fmt.Errorf("config: COSTUMETREE_DIR must not be empty")
		}
		settings.CostumeTreeDir = filepath.Clean(value)
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
