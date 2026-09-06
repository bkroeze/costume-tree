package web

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"

	"costume-tree/internal/storage"
)

//go:embed templates/dashboard.html
var dashboardTemplates embed.FS

// DashboardPageModel is the production dashboard view model.
type DashboardPageModel struct {
	Title      string
	Production storage.Production
	KPIs       storage.ProductionKPIs
	Actors     []storage.ActorSummary
	Statuses   []string
	Empty      bool
	Error      string
}

// WorkspaceItemView is the presentation-safe active item shown in an actor
// workspace. UpdatedAt is carried through forms for stale-write detection.
type WorkspaceItemView struct {
	ID          int64
	Code        string
	Description string
	Status      string
	Progress    int
	NextAction  string
	Blocker     string
	Blocked     bool
	Notes       string
	UpdatedAt   string
}

// WorkspacePageModel is the actor workspace view model.
type WorkspacePageModel struct {
	Title      string
	Production storage.Production
	Actor      storage.Actor
	Items      []WorkspaceItemView
	Statuses   []string
	Errors     FieldErrors
	Error      string
	Success    string
	Empty      bool
}

// DashboardHandler serves production KPI/actor summary pages and actor
// workspaces. It deliberately accepts repository contracts so the composition
// root can wire routes without coupling this feature to a mux.
type DashboardHandler struct {
	productions storage.ProductionRepository
	actors      storage.ActorRepository
	items       storage.CostumeItemRepository
	queries     storage.DashboardQueries
	pages       *template.Template
}

// NewDashboardHandler constructs dashboard and workspace handlers. A supplied
// template set is extended with dashboard templates; when omitted, this
// feature parses its own templates.
func NewDashboardHandler(productions storage.ProductionRepository, actors storage.ActorRepository, items storage.CostumeItemRepository, queries storage.DashboardQueries, pages ...*template.Template) *DashboardHandler {
	parsed := mustDashboardTemplates()
	if len(pages) > 0 && pages[0] != nil {
		parsed = pages[0]
		if _, err := parsed.ParseFS(dashboardTemplates, "templates/dashboard.html"); err != nil {
			parsed = mustDashboardTemplates()
		}
	}
	return &DashboardHandler{productions: productions, actors: actors, items: items, queries: queries, pages: parsed}
}

// NewProductionDashboardHandler is a descriptive constructor alias.
func NewProductionDashboardHandler(productions storage.ProductionRepository, actors storage.ActorRepository, items storage.CostumeItemRepository, queries storage.DashboardQueries, pages ...*template.Template) *DashboardHandler {
	return NewDashboardHandler(productions, actors, items, queries, pages...)
}

func mustDashboardTemplates() *template.Template {
	parsed, err := template.ParseFS(dashboardTemplates, "templates/dashboard.html")
	if err != nil {
		panic(fmt.Sprintf("web: parse dashboard templates: %v", err))
	}
	return parsed
}

// Dashboard renders the production dashboard for GET requests.
func (h *DashboardHandler) Dashboard(w http.ResponseWriter, r *http.Request) error {
	if r.Method != http.MethodGet {
		return dashboardMethodError("dashboard requires GET")
	}
	productionID, err := costumeProductionID(r)
	if err != nil {
		return err
	}
	production, err := h.production(r.Context(), productionID)
	if err != nil {
		return err
	}
	if h.queries == nil {
		return dashboardStorageError("dashboard queries", errors.New("web: dashboard query repository is required"))
	}
	kpis, err := h.queries.ProductionKPIs(r.Context(), productionID)
	if err != nil {
		return dashboardStorageError("load dashboard kpis", err)
	}
	actors, err := h.queries.ActorSummaries(r.Context(), productionID)
	if err != nil {
		return dashboardStorageError("load dashboard actor summaries", err)
	}
	model := DashboardPageModel{Title: "Dashboard · " + production.Name, Production: production, KPIs: kpis, Actors: actors, Statuses: costumeItemStatuses, Empty: len(actors) == 0}
	return h.render(w, r, http.StatusOK, "dashboard-page", "dashboard-content", model)
}

// ProductionDashboard is a conventional alias for mux composition roots.
func (h *DashboardHandler) ProductionDashboard(w http.ResponseWriter, r *http.Request) error {
	return h.Dashboard(w, r)
}

// Workspace renders an actor's active items on GET and accepts a status/
// progress update on POST. Forms must include updated_at; stale writes return
// 409 using the same timestamp policy as the item editor.
func (h *DashboardHandler) Workspace(w http.ResponseWriter, r *http.Request) error {
	productionID, actorID, err := dashboardScopeIDs(r)
	if err != nil {
		return err
	}
	production, actor, err := h.scope(r.Context(), productionID, actorID)
	if err != nil {
		return err
	}
	if r.Method == http.MethodGet {
		return h.renderWorkspace(w, r, http.StatusOK, production, actor, WorkspacePageModel{})
	}
	if r.Method != http.MethodPost {
		return dashboardMethodError("workspace requires GET or POST")
	}
	if h.items == nil {
		return dashboardStorageError("workspace items", errors.New("web: costume item repository is required"))
	}
	if err := r.ParseForm(); err != nil {
		return &Error{Status: http.StatusBadRequest, Message: "Unable to read the workspace form.", Err: fmt.Errorf("parse workspace form: %w", err)}
	}
	itemID, err := parsePositive(r.FormValue("item_id"))
	if err != nil {
		return &Error{Status: http.StatusBadRequest, Message: "Choose a valid costume item.", Err: fmt.Errorf("web: invalid workspace item id: %w", err)}
	}
	item, err := h.items.Get(r.Context(), productionID, itemID)
	if err != nil {
		return dashboardStorageError("get workspace item", err)
	}
	if item.ActorID != actorID || item.ProductionID != productionID {
		return &Error{Status: http.StatusNotFound, Message: "Costume item not found.", Err: storage.ErrNotFound}
	}
	if item.ArchivedAt != nil {
		return dashboardStorageError("update workspace item", storage.ErrArchived)
	}
	updatedAt := strings.TrimSpace(r.FormValue("updated_at"))
	if updatedAt == "" {
		updatedAt = strings.TrimSpace(r.FormValue("updatedAt"))
	}
	if updatedAt == "" || !sameTimestamp(updatedAt, item.UpdatedAt) {
		model := WorkspacePageModel{Errors: FieldErrors{"updated_at": "This item changed in another window. Reload before saving."}}
		return h.renderWorkspace(w, r, http.StatusConflict, production, actor, model)
	}
	status := strings.TrimSpace(r.FormValue("status"))
	if status == "" {
		status = item.Status
	}
	progress := item.Progress
	if value := strings.TrimSpace(r.FormValue("progress")); value != "" {
		progress, err = strconv.Atoi(value)
		if err != nil || progress < 0 || progress > 100 {
			return h.workspaceValidation(w, r, production, actor, "Progress must be a whole number from 0 to 100.", "progress")
		}
	}
	if !containsStatus(status) {
		return h.workspaceValidation(w, r, production, actor, "Choose a valid status.", "status")
	}
	if status == storage.StatusComplete && progress != 100 {
		return h.workspaceValidation(w, r, production, actor, "Complete items must be at 100%.", "progress")
	}
	updated, err := h.items.Update(r.Context(), storage.UpdateCostumeItemInput{
		ProductionID: productionID, ID: item.ID, ActorID: actorID, ItemTypeID: item.ItemTypeID,
		Description: item.Description, Status: status, Progress: progress, NextAction: item.NextAction,
		Blocker: item.Blocker, Notes: item.Notes, ExpectedUpdatedAt: &item.UpdatedAt,
	})
	if err != nil {
		return dashboardStorageError("update workspace item", err)
	}
	SetTrigger(w, "workspace:item-updated")
	if !IsHTMX(r) {
		Redirect(w, r, workspacePath(productionID, actorID), http.StatusSeeOther)
		return nil
	}
	model := WorkspacePageModel{Success: updated.Code + " updated."}
	return h.renderWorkspace(w, r, http.StatusOK, production, actor, model)
}

// ActorWorkspace is a conventional alias for mux composition roots.
func (h *DashboardHandler) ActorWorkspace(w http.ResponseWriter, r *http.Request) error {
	return h.Workspace(w, r)
}

func (h *DashboardHandler) production(ctx context.Context, id int64) (storage.Production, error) {
	if h.productions == nil {
		return storage.Production{}, dashboardStorageError("get production", errors.New("web: production repository is required"))
	}
	production, err := h.productions.Get(ctx, id)
	if err != nil {
		return storage.Production{}, dashboardStorageError("get production", err)
	}
	if production.ArchivedAt != nil {
		return storage.Production{}, &Error{Status: http.StatusConflict, Message: "That production is archived.", Err: storage.ErrArchived}
	}
	return production, nil
}

func (h *DashboardHandler) scope(ctx context.Context, productionID, actorID int64) (storage.Production, storage.Actor, error) {
	production, err := h.production(ctx, productionID)
	if err != nil {
		return storage.Production{}, storage.Actor{}, err
	}
	if h.actors == nil {
		return storage.Production{}, storage.Actor{}, dashboardStorageError("get actor", errors.New("web: actor repository is required"))
	}
	actor, err := h.actors.Get(ctx, productionID, actorID)
	if err != nil {
		return storage.Production{}, storage.Actor{}, dashboardStorageError("get actor", err)
	}
	if actor.ArchivedAt != nil {
		return storage.Production{}, storage.Actor{}, &Error{Status: http.StatusConflict, Message: "That actor is archived.", Err: storage.ErrArchived}
	}
	return production, actor, nil
}

func (h *DashboardHandler) renderWorkspace(w http.ResponseWriter, r *http.Request, status int, production storage.Production, actor storage.Actor, overlay WorkspacePageModel) error {
	if h.items == nil {
		return dashboardStorageError("list workspace items", errors.New("web: costume item repository is required"))
	}
	items, err := h.items.List(r.Context(), storage.CostumeItemFilter{ProductionID: production.ID, ActorID: actor.ID})
	if err != nil {
		return dashboardStorageError("list workspace items", err)
	}
	model := overlay
	model.Title = "Workspace · " + actor.Name
	model.Production = production
	model.Actor = actor
	model.Statuses = costumeItemStatuses
	model.Items = make([]WorkspaceItemView, 0, len(items))
	for _, item := range items {
		model.Items = append(model.Items, WorkspaceItemView{ID: item.ID, Code: item.Code, Description: item.Description, Status: item.Status, Progress: item.Progress, NextAction: item.NextAction, Blocker: item.Blocker, Blocked: strings.TrimSpace(item.Blocker) != "", Notes: item.Notes, UpdatedAt: formatUpdatedAt(item.UpdatedAt)})
	}
	model.Empty = len(model.Items) == 0
	return h.render(w, r, status, "workspace-page", "workspace-content", model)
}

func (h *DashboardHandler) workspaceValidation(w http.ResponseWriter, r *http.Request, production storage.Production, actor storage.Actor, message, field string) error {
	return h.renderWorkspace(w, r, http.StatusUnprocessableEntity, production, actor, WorkspacePageModel{Errors: FieldErrors{field: message}})
}

func (h *DashboardHandler) render(w http.ResponseWriter, r *http.Request, status int, fullName, fragmentName string, model any) error {
	if h.pages == nil {
		return &Error{Status: http.StatusInternalServerError, Message: "Unable to render the page.", Err: errors.New("web: template set is required")}
	}
	if IsHTMX(r) {
		return RenderFragment(w, h.pages, fragmentName, status, model)
	}
	var page bytes.Buffer
	if err := h.pages.ExecuteTemplate(&page, fullName, model); err != nil {
		return &Error{Status: http.StatusInternalServerError, Message: "Unable to render the page.", Err: fmt.Errorf("render %s: %w", fullName, err)}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, err := page.WriteTo(w)
	return err
}

func dashboardStorageError(operation string, err error) error {
	status, message := http.StatusInternalServerError, "Unable to load the dashboard."
	if errors.Is(err, storage.ErrNotFound) {
		status, message = http.StatusNotFound, "That production, actor, or costume item was not found."
	}
	if errors.Is(err, storage.ErrArchived) {
		status, message = http.StatusConflict, "That record is archived."
	}
	return &Error{Status: status, Message: message, Err: fmt.Errorf("%s: %w", operation, err)}
}

func dashboardMethodError(detail string) error {
	return &Error{Status: http.StatusMethodNotAllowed, Message: "Method not allowed.", Err: errors.New("web: " + detail)}
}

func dashboardScopeIDs(r *http.Request) (int64, int64, error) {
	productionID, err := costumeProductionID(r)
	if err != nil {
		return 0, 0, err
	}
	values := []string{r.PathValue("actor"), r.URL.Query().Get("actor_id")}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	for i, part := range parts {
		if part == "workspace" && i+1 < len(parts) {
			values = append(values, parts[i+1])
			break
		}
	}
	for _, value := range values {
		if value == "" {
			continue
		}
		actorID, parseErr := parsePositive(value)
		if parseErr != nil {
			return 0, 0, &Error{Status: http.StatusBadRequest, Message: "Choose a valid actor.", Err: parseErr}
		}
		return productionID, actorID, nil
	}
	return 0, 0, &Error{Status: http.StatusBadRequest, Message: "An actor is required.", Err: errors.New("web: missing actor id")}
}

func parsePositive(value string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("must be a positive integer")
	}
	return id, nil
}
func workspacePath(productionID, actorID int64) string {
	return "/production/" + strconv.FormatInt(productionID, 10) + "/workspace/" + strconv.FormatInt(actorID, 10)
}
