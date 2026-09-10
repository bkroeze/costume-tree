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
	handler, err := New(slog.New(slog.NewTextHandler(io.Discard, nil)), Dependencies{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}


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
