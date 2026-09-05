package config

import (
	"testing"
	"time"
)

func TestLoadDefaultsAndOverrides(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		settings, err := Load(func(string) (string, bool) { return "", false })
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if settings.Address != ":8080" {
			t.Errorf("Address = %q, want :8080", settings.Address)
		}
		if settings.DatabasePath != "/data/costume-tree.db" {
			t.Errorf("DatabasePath = %q, want /data/costume-tree.db", settings.DatabasePath)
		}
		if settings.ShutdownTimeout != 10*time.Second {
			t.Errorf("ShutdownTimeout = %v, want 10s", settings.ShutdownTimeout)
		}
	})

	t.Run("overrides", func(t *testing.T) {
		values := map[string]string{
			"COSTUME_TREE_ADDR":             "127.0.0.1:9090",
			"COSTUME_TREE_DB_PATH":          "/data/test.db",
			"COSTUME_TREE_SHUTDOWN_TIMEOUT": "3s",
		}
		settings, err := Load(func(key string) (string, bool) {
			value, ok := values[key]
			return value, ok
		})
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if settings.Address != "127.0.0.1:9090" {
			t.Errorf("Address = %q, want 127.0.0.1:9090", settings.Address)
		}
		if settings.DatabasePath != "/data/test.db" {
			t.Errorf("DatabasePath = %q, want /data/test.db", settings.DatabasePath)
		}
		if settings.ShutdownTimeout != 3*time.Second {
			t.Errorf("ShutdownTimeout = %v, want 3s", settings.ShutdownTimeout)
		}
	})
}

func TestLoadRejectsDatabasePathOutsideData(t *testing.T) {
	for _, path := range []string{"", "costume-tree.db", "/tmp/costume-tree.db", "/data/../tmp/db"} {
		t.Run(path, func(t *testing.T) {
			_, err := Load(func(key string) (string, bool) {
				if key == "COSTUME_TREE_DB_PATH" {
					return path, true
				}
				return "", false
			})
			if err == nil {
				t.Fatalf("Load() with path %q returned nil error", path)
			}
		})
	}
}

func TestLoadRejectsInvalidShutdownTimeout(t *testing.T) {
	_, err := Load(func(key string) (string, bool) {
		if key == "COSTUME_TREE_SHUTDOWN_TIMEOUT" {
			return "never", true
		}
		return "", false
	})
	if err == nil {
		t.Fatal("Load() error = nil, want invalid duration error")
	}
}
