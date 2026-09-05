// Package web owns HTTP presentation: routing, embedded templates and assets,
// and the single boundary that logs handler failures. Domain and persistence
// behavior belong to feature packages injected by the command composition root.
package web

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
)

//go:embed templates/*.html assets/*
var content embed.FS

type handlerFunc func(http.ResponseWriter, *http.Request) error

// Error describes an HTTP failure while retaining its internal cause.
type Error struct {
	Status  int
	Message string
	Err     error
}

func (e *Error) Error() string {
	return e.Err.Error()
}

func (e *Error) Unwrap() error {
	return e.Err
}

// New constructs the application HTTP handler from embedded content.
func New(logger *slog.Logger) (http.Handler, error) {
	if logger == nil {
		return nil, errors.New("web: logger is required")
	}

	pages, err := template.ParseFS(content, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("web: parse templates: %w", err)
	}
	assets, err := fs.Sub(content, "assets")
	if err != nil {
		return nil, fmt.Errorf("web: open embedded assets: %w", err)
	}

	server := &server{logger: logger, pages: pages}
	mux := http.NewServeMux()
	mux.Handle("GET /assets/", cacheAssets(http.StripPrefix("/assets/", http.FileServer(http.FS(assets)))))
	mux.Handle("GET /{$}", server.handle("home", server.home))
	return mux, nil
}

type server struct {
	logger *slog.Logger
	pages  *template.Template
}

func (s *server) home(w http.ResponseWriter, _ *http.Request) error {
	var page bytes.Buffer
	if err := s.pages.ExecuteTemplate(&page, "layout", struct{ Title string }{Title: "Costume Tree"}); err != nil {
		return &Error{Status: http.StatusInternalServerError, Message: "Unable to render the page.", Err: fmt.Errorf("render home: %w", err)}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, err := page.WriteTo(w)
	return err
}

func (s *server) handle(name string, next handlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := next(w, r); err != nil {
			status := http.StatusInternalServerError
			message := "Internal server error."
			var httpError *Error
			if errors.As(err, &httpError) {
				status = httpError.Status
				message = httpError.Message
			}
			s.logger.Error("HTTP request failed",
				"handler", name,
				"method", r.Method,
				"path", r.URL.Path,
				"status", status,
				"error", err,
			)
			http.Error(w, message, status)
		}
	}
}

func cacheAssets(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		next.ServeHTTP(w, r)
	})
}
