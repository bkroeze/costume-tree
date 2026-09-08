package web

import (
	"context"
	"encoding/json"
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
	handler, err := New(slog.New(slog.NewTextHandler(io.Discard, nil)), Dependencies{})
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
		body := response.Body.String()
		if !strings.Contains(body, `href="/assets/app.css`) {
			t.Errorf("body does not reference embedded stylesheet: %s", body)
		}
		if !strings.Contains(body, `rel="manifest" href="/assets/site.webmanifest"`) {
			t.Errorf("body does not reference web app manifest: %s", body)
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

	t.Run("manifest declares installable app", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodGet, "/assets/site.webmanifest", nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)

		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
		}
		if contentType := response.Header().Get("Content-Type"); contentType != "application/manifest+json" {
			t.Errorf("Content-Type = %q", contentType)
		}

		var manifest struct {
			ID       string `json:"id"`
			StartURL string `json:"start_url"`
			Scope    string `json:"scope"`
			Display  string `json:"display"`
			Icons    []struct {
				Sizes string `json:"sizes"`
			} `json:"icons"`
		}
		if err := json.NewDecoder(response.Body).Decode(&manifest); err != nil {
			t.Fatalf("decode manifest: %v", err)
		}
		if manifest.ID != "/" || manifest.StartURL != "/" || manifest.Scope != "/" {
			t.Errorf("navigation identity = id %q, start_url %q, scope %q; want root", manifest.ID, manifest.StartURL, manifest.Scope)
		}
		if manifest.Display != "standalone" {
			t.Errorf("display = %q, want standalone", manifest.Display)
		}
		iconSizes := make(map[string]bool, len(manifest.Icons))
		for _, icon := range manifest.Icons {
			iconSizes[icon.Sizes] = true
		}
		if !iconSizes["192x192"] || !iconSizes["512x512"] {
			t.Errorf("icon sizes = %v, want 192x192 and 512x512", iconSizes)
		}
	})
}

func TestHealthzReflectsDatabaseReadiness(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	t.Run("ready", func(t *testing.T) {
		handler, err := New(logger, Dependencies{Readiness: readinessStub{}})
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
		handler, err := New(logger, Dependencies{Readiness: readinessStub{err: errors.New("database unavailable")}})
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

func TestDemoFormSupportsFullPageAndHTMXResponses(t *testing.T) {
	handler, err := New(slog.New(slog.NewTextHandler(io.Discard, nil)), Dependencies{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	t.Run("full page validation error", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodPost, "/demo", strings.NewReader("piece="))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)

		if response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want %d", response.Code, http.StatusUnprocessableEntity)
		}
		body := response.Body.String()
		if !strings.Contains(body, "Enter a piece name") {
			t.Errorf("body lacks validation error: %s", body)
		}
		if !strings.Contains(body, "<html") {
			t.Errorf("full-page response lacks document shell: %s", body)
		}
	})

	t.Run("HTMX success fragment", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodPost, "/demo", strings.NewReader("piece=Velvet+cape"))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("HX-Request", "true")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)

		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
		}
		if trigger := response.Header().Get("HX-Trigger"); trigger != "demo:submitted" {
			t.Errorf("HX-Trigger = %q, want demo:submitted", trigger)
		}
		body := response.Body.String()
		if !strings.Contains(body, "Preview saved") {
			t.Errorf("body lacks success message: %s", body)
		}
		if strings.Contains(body, "<html") {
			t.Errorf("HTMX response unexpectedly contains document shell: %s", body)
		}
	})
}

func TestVendorAssetsAreEmbedded(t *testing.T) {
	handler, err := New(slog.New(slog.NewTextHandler(io.Discard, nil)), Dependencies{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	for _, asset := range []string{"htmx.min.js", "alpine.min.js"} {
		t.Run(asset, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/assets/"+asset, nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
			}
			if response.Body.Len() < 1000 {
				t.Fatalf("embedded asset is unexpectedly small: %d bytes", response.Body.Len())
			}
		})
	}
}
