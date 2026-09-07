package web

import (
	"bytes"
	"fmt"
	"html/template"
	"net/http"
	"sort"
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
	productions storage.ProductionRepository
	actors      storage.ActorRepository
	queries     storage.DashboardQueries
	pages       *template.Template
}

// NewLandingHandler constructs a new LandingHandler.
func NewLandingHandler(
	productions storage.ProductionRepository,
	actors storage.ActorRepository,
	queries storage.DashboardQueries,
	pages *template.Template,
) *LandingHandler {
	return &LandingHandler{
		productions: productions,
		actors:      actors,
		queries:     queries,
		pages:       pages,
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

	if h.productions != nil {
		productions, err := h.productions.List(ctx)
		if err != nil {
			return &Error{
				Status:  http.StatusInternalServerError,
				Message: "Unable to list productions.",
				Err:     fmt.Errorf("landing: list productions: %w", err),
			}
		}

		// Sort productions alphabetically
		sort.SliceStable(productions, func(i, j int) bool {
			if productions[i].Name == productions[j].Name {
				return productions[i].ID < productions[j].ID
			}
			return productions[i].Name < productions[j].Name
		})

		for _, prod := range productions {
			if prod.ArchivedAt != nil {
				continue
			}

			card := ShowCardView{
				ID:           prod.ID,
				Name:         prod.Name,
				Archived:     prod.ArchivedAt != nil,
				DashboardURL: "/production/" + strconv.FormatInt(prod.ID, 10) + "/dashboard",
				ActorsURL:    "/production/" + strconv.FormatInt(prod.ID, 10) + "/actors",
			}

			if h.actors != nil {
				actList, err := h.actors.List(ctx, prod.ID, false)
				if err != nil {
					return &Error{
						Status:  http.StatusInternalServerError,
						Message: "Unable to list actors.",
						Err:     fmt.Errorf("landing: list actors for production %d: %w", prod.ID, err),
					}
				}
				card.ActorCount = len(actList)
				model.TotalActors += len(actList)
			}

			if h.queries != nil {
				kpis, err := h.queries.ProductionKPIs(ctx, prod.ID)
				if err != nil {
					return &Error{
						Status:  http.StatusInternalServerError,
						Message: "Unable to load production totals.",
						Err:     fmt.Errorf("landing: load KPIs for production %d: %w", prod.ID, err),
					}
				}
				card.PieceCount = kpis.TotalActivePieces
				card.BlockedCount = kpis.Blocked
				model.TotalPieces += kpis.TotalActivePieces
			}

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
