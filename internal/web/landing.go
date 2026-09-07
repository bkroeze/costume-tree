package web

import (
	"bytes"
	"fmt"
	"html/template"
	"net/http"
	"strconv"

	"costume-tree/internal/storage"
)

// ShowCardView represents a production/show on the landing page directory.
type ShowCardView struct {
	ID           int64
	Name         string
	Archived     bool
	ActorCount   int
	PieceCount   int
	BlockedCount int
	DashboardURL string
	ActorsURL    string
}

// LandingPageModel contains all view state needed to render the landing page.
type LandingPageModel struct {
	Title       string
	Shows       []ShowCardView
	HasShows    bool
	TotalShows  int
	TotalActors int
	TotalPieces int
	Demo        demoState
}

// LandingHandler serves the main landing page featuring the Option B Hero
// showcase and the directory linking to all productions, their dashboard
// detail pages, and their actor list pages.
type LandingHandler struct {
	directory storage.ProductionDirectoryQueries
	pages     *template.Template
}

// NewLandingHandler constructs a new LandingHandler.
func NewLandingHandler(
	directory storage.ProductionDirectoryQueries,
	pages *template.Template,
) *LandingHandler {
	return &LandingHandler{
		directory: directory,
		pages:     pages,
	}
}

// Landing handles GET /{$} by displaying the Option B hero showcase and all shows.
func (h *LandingHandler) Landing(w http.ResponseWriter, r *http.Request) error {
	return h.render(w, r, http.StatusOK, demoState{})
}

func (h *LandingHandler) render(w http.ResponseWriter, r *http.Request, status int, demo demoState) error {
	ctx := r.Context()
	model := LandingPageModel{
		Title: "Costume Tree — Wardrobe Operations",
		Demo:  demo,
	}

	if h.directory != nil {
		entries, err := h.directory.ProductionDirectory(ctx)
		if err != nil {
			return &Error{
				Status:  http.StatusInternalServerError,
				Message: "Unable to load the production directory.",
				Err:     fmt.Errorf("landing: load production directory: %w", err),
			}
		}

		for _, entry := range entries {
			card := ShowCardView{
				ID:           entry.ID,
				Name:         entry.Name,
				ActorCount:   entry.ActorCount,
				PieceCount:   entry.PieceCount,
				BlockedCount: entry.BlockedCount,
				DashboardURL: "/production/" + strconv.FormatInt(entry.ID, 10) + "/dashboard",
				ActorsURL:    "/production/" + strconv.FormatInt(entry.ID, 10) + "/actors",
			}
			model.TotalActors += entry.ActorCount
			model.TotalPieces += entry.PieceCount
			model.Shows = append(model.Shows, card)
		}
		model.TotalShows = len(model.Shows)
		model.HasShows = len(model.Shows) > 0
	}

	var page bytes.Buffer
	if err := h.pages.ExecuteTemplate(&page, "layout", model); err != nil {
		return &Error{
			Status:  http.StatusInternalServerError,
			Message: "Unable to render landing page.",
			Err:     fmt.Errorf("render landing page: %w", err),
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, err := page.WriteTo(w)
	return err
}
