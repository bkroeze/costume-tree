package config

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestLoadDefaultsAndOverrides(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		settings, err := Load(postgresLookup(nil))
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if settings.Address != ":8080" {
			t.Errorf("Address = %q, want :8080", settings.Address)
		}
		if settings.PGHost != "database.example" {
			t.Errorf("PGHost = %q, want database.example", settings.PGHost)
		}
		if settings.PGPort != 5432 {
			t.Errorf("PGPort = %d, want 5432", settings.PGPort)
		}
		if settings.PGUser != "costume-tree" {
			t.Errorf("PGUser = %q, want costume-tree", settings.PGUser)
		}
		if settings.PGPassword != "secret" {
			t.Errorf("PGPassword = %q, want secret", settings.PGPassword)
		}
		if settings.PGDatabase != "costume_tree" {
			t.Errorf("PGDatabase = %q, want costume_tree", settings.PGDatabase)
		}
		if settings.PGSSLMode != "prefer" {
			t.Errorf("PGSSLMode = %q, want prefer", settings.PGSSLMode)
		}
		if settings.DBMaxOpenConns != 20 {
			t.Errorf("DBMaxOpenConns = %d, want 20", settings.DBMaxOpenConns)
		}
		if settings.CostumeTreeDir != "." {
			t.Errorf("CostumeTreeDir = %q, want .", settings.CostumeTreeDir)
		}
		if settings.ShutdownTimeout != 10*time.Second {
			t.Errorf("ShutdownTimeout = %v, want 10s", settings.ShutdownTimeout)
		}
		if settings.RequestTimeout != 30*time.Second {
			t.Errorf("RequestTimeout = %v, want 30s", settings.RequestTimeout)
		}
		if settings.MaxBodyBytes != 24<<20 {
			t.Errorf("MaxBodyBytes = %d, want 24 MiB", settings.MaxBodyBytes)
		}
	})

	t.Run("overrides", func(t *testing.T) {
		values := map[string]string{
			"COSTUME_TREE_ADDR":             "127.0.0.1:9090",
			"PG_HOST":                       "2001:db8::1",
			"PG_PORT":                       "6432",
			"PG_USER":                       "wardrobe",
			"PG_PASSWORD":                   "different-secret",
			"PG_DATABASE":                   "wardrobe_test",
			"PG_SSLMODE":                    "verify-full",
			"DB_MAX_OPEN_CONNS":             "12",
			"COSTUMETREE_DIR":               "/srv/photos/../costume-tree/",
			"COSTUME_TREE_SHUTDOWN_TIMEOUT": "3s",
			"COSTUME_TREE_REQUEST_TIMEOUT":  "7s",
			"COSTUME_TREE_MAX_BODY_BYTES":   "2048",
		}
		settings, err := Load(postgresLookup(values))
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if settings.Address != "127.0.0.1:9090" {
			t.Errorf("Address = %q, want 127.0.0.1:9090", settings.Address)
		}
		if settings.PGHost != "2001:db8::1" || settings.PGPort != 6432 {
			t.Errorf("PostgreSQL address = %q:%d, want [2001:db8::1]:6432", settings.PGHost, settings.PGPort)
		}
		if settings.PGDatabase != "wardrobe_test" || settings.PGSSLMode != "verify-full" {
			t.Errorf("PostgreSQL target = %q sslmode=%q, want wardrobe_test sslmode=verify-full", settings.PGDatabase, settings.PGSSLMode)
		}
		if settings.DBMaxOpenConns != 12 {
			t.Errorf("DBMaxOpenConns = %d, want 12", settings.DBMaxOpenConns)
		}
		if settings.CostumeTreeDir != "/srv/costume-tree" {
			t.Errorf("CostumeTreeDir = %q, want /srv/costume-tree", settings.CostumeTreeDir)
		}
		if settings.ShutdownTimeout != 3*time.Second {
			t.Errorf("ShutdownTimeout = %v, want 3s", settings.ShutdownTimeout)
		}
		if settings.RequestTimeout != 7*time.Second {
			t.Errorf("RequestTimeout = %v, want 7s", settings.RequestTimeout)
		}
		if settings.MaxBodyBytes != 2048 {
			t.Errorf("MaxBodyBytes = %d, want 2048", settings.MaxBodyBytes)
		}
	})
}

func TestLoadRequiresPostgresCredentials(t *testing.T) {
	for _, name := range []string{"PG_HOST", "PG_USER", "PG_PASSWORD"} {
		t.Run(name+" missing", func(t *testing.T) {
			values := requiredPostgresValues()
			delete(values, name)
			if _, err := Load(mapLookup(values)); err == nil {
				t.Fatalf("Load() error = nil, want missing %s error", name)
			}
		})
		t.Run(name+" empty", func(t *testing.T) {
			values := requiredPostgresValues()
			values[name] = ""
			if _, err := Load(postgresLookup(values)); err == nil {
				t.Fatalf("Load() error = nil, want empty %s error", name)
			}
		})
	}
}

func TestLoadRejectsInvalidPostgresOptions(t *testing.T) {
	for name, value := range map[string]string{
		"PG_PORT":           "0",
		"PG_DATABASE":       "",
		"PG_SSLMODE":        "",
		"DB_MAX_OPEN_CONNS": "0",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(postgresLookup(map[string]string{name: value})); err == nil {
				t.Fatalf("Load() with %s=%q returned nil error", name, value)
			}
		})
	}
}

func TestPostgresURLEscapesConnectionFields(t *testing.T) {
	settings := Settings{
		PGHost:     "2001:db8::1",
		PGPort:     5432,
		PGUser:     "wardrobe@example.com",
		PGPassword: "s/ecret:@ word",
		PGDatabase: "show/2026 wardrobe",
		PGSSLMode:  "verify-full",
	}

	connectionString := settings.PostgresURL()
	if strings.Contains(connectionString, settings.PGPassword) {
		t.Fatalf("PostgresURL() = %q, contains unescaped password", connectionString)
	}
	parsed, err := url.Parse(connectionString)
	if err != nil {
		t.Fatalf("parse PostgresURL(): %v", err)
	}
	password, ok := parsed.User.Password()
	if !ok || parsed.User.Username() != settings.PGUser || password != settings.PGPassword {
		t.Errorf("PostgresURL() credentials did not round trip")
	}
	if parsed.Hostname() != settings.PGHost || parsed.Port() != "5432" {
		t.Errorf("PostgresURL() host = %q, want [%s]:5432", parsed.Host, settings.PGHost)
	}
	if strings.TrimPrefix(parsed.Path, "/") != settings.PGDatabase {
		t.Errorf("PostgresURL() database path = %q, want %q", parsed.Path, settings.PGDatabase)
	}
	if parsed.Query().Get("sslmode") != settings.PGSSLMode {
		t.Errorf("PostgresURL() sslmode = %q, want %q", parsed.Query().Get("sslmode"), settings.PGSSLMode)
	}
	if parsed.Query().Get("search_path") != "public" {
		t.Errorf("PostgresURL() search_path = %q, want public", parsed.Query().Get("search_path"))
	}
	postgresConfig, err := pgx.ParseConfig(connectionString)
	if err != nil {
		t.Fatalf("pgx.ParseConfig(PostgresURL()): %v", err)
	}
	if postgresConfig.RuntimeParams["search_path"] != "public" {
		t.Errorf("PostgresURL() pgx search_path = %q, want public", postgresConfig.RuntimeParams["search_path"])
	}
}

func TestLoadRejectsEmptyCostumeTreeDir(t *testing.T) {
	_, err := Load(postgresLookup(map[string]string{"COSTUMETREE_DIR": ""}))
	if err == nil {
		t.Fatal("Load() error = nil, want empty COSTUMETREE_DIR error")
	}
}

func TestLoadRejectsInvalidShutdownTimeout(t *testing.T) {
	_, err := Load(postgresLookup(map[string]string{"COSTUME_TREE_SHUTDOWN_TIMEOUT": "never"}))
	if err == nil {
		t.Fatal("Load() error = nil, want invalid duration error")
	}
}

func TestLoadRejectsUnboundedHTTPSettings(t *testing.T) {
	for name, value := range map[string]string{
		"COSTUME_TREE_REQUEST_TIMEOUT": "0s",
		"COSTUME_TREE_MAX_BODY_BYTES":  "67108865",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Load(postgresLookup(map[string]string{name: value}))
			if err == nil {
				t.Fatalf("Load() with %s=%q returned nil error", name, value)
			}
		})
	}
}

func postgresLookup(overrides map[string]string) func(string) (string, bool) {
	values := requiredPostgresValues()
	for key, value := range overrides {
		values[key] = value
	}
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

func mapLookup(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

func requiredPostgresValues() map[string]string {
	return map[string]string{
		"PG_HOST":     "database.example",
		"PG_USER":     "costume-tree",
		"PG_PASSWORD": "secret",
	}
}
