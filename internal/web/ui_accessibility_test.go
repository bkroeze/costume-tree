package web

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAccessibleInteractionScaffold(t *testing.T) {
	handler, err := New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	t.Run("home form keeps progressive enhancement and live feedback", func(t *testing.T) {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
		body := response.Body.String()
		for _, want := range []string{
			`<form id="demo-form"`,
			`action="/demo" method="post"`,
			`hx-swap="innerHTML"`,
			`id="demo-feedback" class="form-feedback" aria-live="polite" aria-atomic="true"`,
			`<label for="piece-name">Piece name</label>`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("home markup missing %q", want)
			}
		}
	})

	t.Run("stylesheet includes interaction and narrow viewport safeguards", func(t *testing.T) {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/assets/app.css", nil))
		css := response.Body.String()
		for _, want := range []string{
			":focus-visible",
			".htmx-request button[type=\"submit\"]",
			".table-wrap",
			"@media (max-width: 440px)",
			"@media (prefers-reduced-motion: reduce)",
		} {
			if !strings.Contains(css, want) {
				t.Errorf("stylesheet missing %q", want)
			}
		}
	})
}
