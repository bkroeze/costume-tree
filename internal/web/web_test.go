package web

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type readinessStub struct {
	err error
}

func (s readinessStub) Ready(context.Context) error {
	return s.err
}

func TestHandlerServesHomeAndEmbeddedAsset(t *testing.T) {
	handler, err := New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	t.Run("home references local stylesheet", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)

		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
		}
		if contentType := response.Header().Get("Content-Type"); contentType != "text/html; charset=utf-8" {
			t.Errorf("Content-Type = %q", contentType)
		}
		if body := response.Body.String(); !strings.Contains(body, `href="/assets/app.css"`) {
			t.Errorf("body does not reference embedded stylesheet: %s", body)
		}
	})

	t.Run("stylesheet comes from embedded filesystem", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodGet, "/assets/app.css", nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)

		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
		}
		if cacheControl := response.Header().Get("Cache-Control"); cacheControl != "public, max-age=3600" {
			t.Errorf("Cache-Control = %q", cacheControl)
		}
		if body := response.Body.String(); !strings.Contains(body, "--cobalt: #2563eb") {
			t.Errorf("unexpected stylesheet body: %s", body)
		}
	})
}

func TestHealthzReflectsDatabaseReadiness(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	t.Run("ready", func(t *testing.T) {
		handler, err := New(logger, readinessStub{})
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}

		request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)

		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
		}
		if body := response.Body.String(); body != "ok\n" {
			t.Errorf("body = %q, want ok", body)
		}
	})

	t.Run("unavailable", func(t *testing.T) {
		handler, err := New(logger, readinessStub{err: errors.New("database unavailable")})
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}

		request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)

		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want %d", response.Code, http.StatusServiceUnavailable)
		}
		if body := response.Body.String(); body != "unhealthy\n" {
			t.Errorf("body = %q, want unhealthy", body)
		}
	})
}
