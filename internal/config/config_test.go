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
		if settings.ShutdownTimeout != 10*time.Second {
			t.Errorf("ShutdownTimeout = %v, want 10s", settings.ShutdownTimeout)
		}
	})

	t.Run("overrides", func(t *testing.T) {
		values := map[string]string{
			"COSTUME_TREE_ADDR":             "127.0.0.1:9090",
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
		if settings.ShutdownTimeout != 3*time.Second {
			t.Errorf("ShutdownTimeout = %v, want 3s", settings.ShutdownTimeout)
		}
	})
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
