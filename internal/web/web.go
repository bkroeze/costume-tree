// Package web owns HTTP presentation: routing, embedded templates and assets,
// and the single boundary that logs handler failures. Domain and persistence
// behavior belong to feature packages injected by the command composition root.
package web

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"

	"costume-tree/internal/storage"
)

//go:embed templates/*.html assets/*
var content embed.FS

type handlerFunc func(http.ResponseWriter, *http.Request) error

// Readiness is the application dependency check used by /healthz.
type Readiness interface {
	Ready(context.Context) error
}

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

type demoState struct {
	Error   string
	Success string
}

type pageModel struct {
	Title string
	Demo  demoState
}

// New constructs the application HTTP handler from embedded content.
// A readiness dependency may be supplied by the composition root. Omitting it
// preserves the no-database scaffold and reports the process as healthy.
func New(logger *slog.Logger, readiness ...Readiness) (http.Handler, error) {
	if logger == nil {
		return nil, errors.New("web: logger is required")
	}
	if len(readiness) > 1 {
		return nil, errors.New("web: only one readiness dependency is supported")
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
	if len(readiness) == 1 {
		server.readiness = readiness[0]
	}

	mux := http.NewServeMux()
	mux.Handle("GET /assets/", cacheAssets(http.StripPrefix("/assets/", http.FileServer(http.FS(assets)))))
	mux.Handle("POST /demo", server.handle("demo", server.demo))
	mux.HandleFunc("GET /healthz", server.health)

	if database, ok := readinessDatabase(readiness); ok {
		productions := storage.NewProductionRepository(database)
		actors := storage.NewActorRepository(database)
		itemTypes := storage.NewItemTypeRepository(database)
		actorHandler := NewActorHandler(productions, actors, pages, itemTypes)
		itemTypeHandler := NewItemTypeHandler(productions, itemTypes, pages)

		mux.Handle("GET /{$}", server.handle("production", actorHandler.Production))
		mux.Handle("GET /production", server.handle("production", actorHandler.Production))
		mux.Handle("POST /production", server.handle("production", actorHandler.Production))
		mux.Handle("GET /actors", server.handle("actors", actorHandler.Actors))
		mux.Handle("POST /actors", server.handle("actors", actorHandler.Actors))
		mux.Handle("GET /actors/{id}", server.handle("actor", actorHandler.EditActor))
		mux.Handle("POST /actors/{id}", server.handle("actor", actorHandler.EditActor))
		mux.Handle("POST /actors/{id}/archive", server.handle("actor-archive", actorHandler.ArchiveActor))

		mux.Handle("GET /production/{production}/item-types", server.handle("item-types", itemTypeHandler.ItemTypes))
		mux.Handle("POST /production/{production}/item-types", server.handle("item-type-create", itemTypeHandler.CreateItemType))
		mux.Handle("POST /production/{production}/item-types/{id}/rename", server.handle("item-type-rename", itemTypeHandler.RenameItemType))
		mux.Handle("POST /production/{production}/item-types/{id}/archive", server.handle("item-type-archive", itemTypeHandler.ArchiveItemType))
		mux.Handle("POST /production/{production}/item-types/{id}/restore", server.handle("item-type-restore", itemTypeHandler.RestoreItemType))
	} else {
		mux.Handle("GET /{$}", server.handle("home", server.home))
	}
	return mux, nil
}

func readinessDatabase(readiness []Readiness) (*storage.DB, bool) {
	if len(readiness) != 1 {
		return nil, false
	}
	database, ok := readiness[0].(*storage.DB)
	return database, ok && database != nil
}

type server struct {
	logger    *slog.Logger
	pages     *template.Template
	readiness Readiness
}

func (s *server) home(w http.ResponseWriter, _ *http.Request) error {
	return s.renderPage(w, http.StatusOK, pageModel{Title: "Costume Tree"})
}

func (s *server) demo(w http.ResponseWriter, r *http.Request) error {
	if err := r.ParseForm(); err != nil {
		return &Error{Status: http.StatusBadRequest, Message: "Unable to read the form.", Err: fmt.Errorf("parse demo form: %w", err)}
	}

	state := demoState{}
	if strings.TrimSpace(r.FormValue("piece")) == "" {
		state.Error = "Enter a piece name before submitting."
	} else {
		state.Success = "Preview saved. The piece is ready for inventory storage."
	}

	status := http.StatusOK
	if state.Error != "" {
		status = http.StatusUnprocessableEntity
	}
	if IsHTMX(r) {
		if state.Success != "" {
			SetTrigger(w, "demo:submitted")
		}
		return RenderFragment(w, s.pages, "demo-feedback", status, pageModel{Title: "Costume Tree", Demo: state})
	}
	return s.renderPage(w, status, pageModel{Title: "Costume Tree", Demo: state})
}

func (s *server) renderPage(w http.ResponseWriter, status int, model pageModel) error {
	var page bytes.Buffer
	if err := s.pages.ExecuteTemplate(&page, "layout", model); err != nil {
		return &Error{Status: http.StatusInternalServerError, Message: "Unable to render the page.", Err: fmt.Errorf("render page: %w", err)}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, err := page.WriteTo(w)
	return err
}

func (s *server) health(w http.ResponseWriter, r *http.Request) {
	if s.readiness != nil {
		if err := s.readiness.Ready(r.Context()); err != nil {
			s.logger.Warn("health check failed", "error", err)
			http.Error(w, "unhealthy", http.StatusServiceUnavailable)
			return
		}
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
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
