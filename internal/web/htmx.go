package web

import (
	"html/template"
	"net/http"
)

// IsHTMX reports whether the request came from an HTMX interaction.
func IsHTMX(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true"
}

// RenderFragment renders a named template for an HTMX response.
func RenderFragment(w http.ResponseWriter, pages *template.Template, name string, status int, data any) error {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	return pages.ExecuteTemplate(w, name, data)
}

// SetTrigger asks HTMX to dispatch a named client event after a response.
func SetTrigger(w http.ResponseWriter, event string) {
	w.Header().Set("HX-Trigger", event)
}

// Redirect performs a normal redirect for non-HTMX requests.
func Redirect(w http.ResponseWriter, r *http.Request, location string, status int) {
	if IsHTMX(r) {
		w.Header().Set("HX-Redirect", location)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, location, status)
}
