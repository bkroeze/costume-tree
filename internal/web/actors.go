package web

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"costume-tree/internal/storage"
)

const (
	// ProductionPath and ActorsPath are the default locations used when a
	// composition root wires these handlers into its mux.
	ProductionPath = "/production"
	ActorsPath     = "/actors"
)

// FieldErrors contains validation messages keyed by form field name.
type FieldErrors map[string]string

// ProductionView is the presentation-safe production state used by templates.
type ProductionView struct {
	ID       int64
	Name     string
	Archived bool
}

// ActorView is the presentation-safe actor state used by templates.
type ActorView struct {
	ID         int64
	Name       string
	Role       string
	Notes      string
	Archived   bool
	Production int64
}

// ProductionFormView carries production form values through validation errors.
type ProductionFormView struct {
	Name   string
	Errors FieldErrors
}

// ActorFormView carries actor form values through validation errors and edit
// requests. Values are deliberately not reconstructed from persistence after a
// validation failure.
type ActorFormView struct {
	ID      int64
	Name    string
	Role    string
	Notes   string
	Editing bool
	Errors  FieldErrors
}

// ActorPageModel is the complete production and actor management view model.
type ActorPageModel struct {
	Title          string
	Production     *ProductionView
	Actors         []ActorView
	ArchivedActors []ActorView
	ProductionForm ProductionFormView
	ActorForm      ActorFormView
	FirstRun       bool
	Empty          bool
	Error          string
}

// ActorHandler owns production bootstrap and actor management HTTP behavior.
// It has no routing concerns so a command composition root can choose its URL
// shape and wrap methods with its own error boundary.
type ActorHandler struct {
	productions storage.ProductionRepository
	actors      storage.ActorRepository
	itemTypes   storage.ItemTypeRepository
	pages       *template.Template
}

// NewActorHandler constructs a production/actor handler from repositories and
// the parsed application templates. An optional item-type repository seeds the
// default vocabulary for each newly created production.
func NewActorHandler(productions storage.ProductionRepository, actors storage.ActorRepository, pages *template.Template, itemTypes ...storage.ItemTypeRepository) *ActorHandler {
	var types storage.ItemTypeRepository
	if len(itemTypes) > 0 {
		types = itemTypes[0]
	}
	return &ActorHandler{productions: productions, actors: actors, itemTypes: types, pages: pages}
}

// ProductionBootstrap renders the active production, or the first-run
// production creation state when no active production exists.
func (h *ActorHandler) ProductionBootstrap(w http.ResponseWriter, r *http.Request) error {
	production, err := h.activeProduction(r.Context())
	if err != nil {
		return err
	}
	if production == nil {
		model := ActorPageModel{Title: "Create your production", FirstRun: true, Empty: true}
		return h.render(w, r, http.StatusOK, "production-page", "production-bootstrap", model)
	}
	return h.renderActors(w, r, production, ActorFormView{})
}

func (h *ActorHandler) NewProduction(w http.ResponseWriter, r *http.Request) error {
	model := ActorPageModel{Title: "Create your production", FirstRun: true, Empty: true}
	return h.render(w, r, http.StatusOK, "production-page", "production-bootstrap", model)
}

// CreateProduction validates and persists a new production.
func (h *ActorHandler) CreateProduction(w http.ResponseWriter, r *http.Request) error {
	if err := r.ParseForm(); err != nil {
		return &Error{Status: http.StatusBadRequest, Message: "Unable to read the production form.", Err: fmt.Errorf("parse production form: %w", err)}
	}
	name := r.FormValue("name")
	if strings.TrimSpace(name) == "" {
		model := ActorPageModel{
			Title:          "Create your production",
			FirstRun:       true,
			Empty:          true,
			ProductionForm: ProductionFormView{Name: name, Errors: FieldErrors{"name": "Enter a production name."}},
		}
		return h.render(w, r, http.StatusUnprocessableEntity, "production-page", "production-form", model)
	}
	if h.productions == nil {
		return &Error{Status: http.StatusInternalServerError, Message: "Production storage is unavailable.", Err: errors.New("web: production repository is required")}
	}
	production, err := h.productions.Create(r.Context(), storage.CreateProductionInput{Name: strings.TrimSpace(name)})
	if err != nil {
		return h.repositoryError("create production", err)
	}
	if h.itemTypes != nil {
		if err := seedDefaultItemTypes(r.Context(), h.itemTypes, production.ID); err != nil {
			return h.repositoryError("seed item types", err)
		}
	}
	SetTrigger(w, "production:created")
	Redirect(w, r, productionActorsPath(production.ID), http.StatusSeeOther)
	return nil
}

// ListActors renders active actors and an archived section for the requested
// production. Legacy routes fall back to the first active production, and a
// missing fallback remains a first-run state.
func (h *ActorHandler) ListActors(w http.ResponseWriter, r *http.Request) error {
	production, err := h.productionForRequest(r)
	if err != nil {
		return err
	}
	if production == nil {
		model := ActorPageModel{Title: "Create your production", FirstRun: true, Empty: true}
		return h.render(w, r, http.StatusOK, "production-page", "production-bootstrap", model)
	}
	return h.renderActors(w, r, production, ActorFormView{})
}

// CreateActor validates and persists an actor in the requested production.
// Actor names are intentionally not checked for uniqueness here; duplicate
// names are valid from the web layer's perspective.
func (h *ActorHandler) CreateActor(w http.ResponseWriter, r *http.Request) error {
	if err := r.ParseForm(); err != nil {
		return &Error{Status: http.StatusBadRequest, Message: "Unable to read the actor form.", Err: fmt.Errorf("parse actor form: %w", err)}
	}
	production, err := h.productionForRequest(r)
	if err != nil {
		return err
	}
	if production == nil {
		model := ActorPageModel{Title: "Create your production", FirstRun: true, Empty: true}
		return h.render(w, r, http.StatusConflict, "production-page", "production-bootstrap", model)
	}
	form := actorFormFromRequest(r, false, 0)
	if strings.TrimSpace(form.Name) == "" {
		form.Errors = FieldErrors{"name": "Enter an actor name."}
		return h.render(w, r, http.StatusUnprocessableEntity, "actor-page", "actor-form", h.modelForProduction(production, form))
	}
	_, err = h.actors.Create(r.Context(), storage.CreateActorInput{
		ProductionID: production.ID,
		Name:         strings.TrimSpace(form.Name),
		Role:         form.Role,
		Notes:        form.Notes,
	})
	if err != nil {
		return h.repositoryError("create actor", err)
	}
	SetTrigger(w, "actor:created")
	if IsHTMX(r) {
		return h.renderActorsFragment(w, r, production, ActorFormView{})
	}
	Redirect(w, r, productionActorsPath(production.ID), http.StatusSeeOther)
	return nil
}

// EditActor renders an actor form for GET and updates an actor for POST. The
// actor ID may come from a mux path value, query parameter, or form field.
func (h *ActorHandler) EditActor(w http.ResponseWriter, r *http.Request) error {
	production, err := h.productionForRequest(r)
	if err != nil {
		return err
	}
	if production == nil {
		return &Error{Status: http.StatusNotFound, Message: "No active production exists.", Err: storage.ErrNotFound}
	}
	id, err := actorID(r)
	if err != nil {
		return &Error{Status: http.StatusBadRequest, Message: "Choose a valid actor.", Err: err}
	}
	actor, err := h.actors.Get(r.Context(), production.ID, id)
	if err != nil {
		return h.repositoryError("get actor", err)
	}
	if actor.ProductionID != production.ID {
		return &Error{Status: http.StatusNotFound, Message: "Actor not found.", Err: storage.ErrNotFound}
	}
	if actor.ArchivedAt != nil {
		return h.repositoryError("edit actor", storage.ErrArchived)
	}
	if r.Method == http.MethodGet {
		return h.render(w, r, http.StatusOK, "actor-page", "actor-form", h.modelForProduction(production, actorFormFromActor(actor)))
	}
	if err := r.ParseForm(); err != nil {
		return &Error{Status: http.StatusBadRequest, Message: "Unable to read the actor form.", Err: fmt.Errorf("parse actor form: %w", err)}
	}
	form := actorFormFromRequest(r, true, id)
	if strings.TrimSpace(form.Name) == "" {
		form.Errors = FieldErrors{"name": "Enter an actor name."}
		return h.render(w, r, http.StatusUnprocessableEntity, "actor-page", "actor-form", h.modelForProduction(production, form))
	}
	_, err = h.actors.Update(r.Context(), storage.UpdateActorInput{
		ProductionID: production.ID,
		ID:           id,
		Name:         strings.TrimSpace(form.Name),
		Role:         form.Role,
		Notes:        form.Notes,
	})
	if err != nil {
		return h.repositoryError("update actor", err)
	}
	SetTrigger(w, "actor:updated")
	if IsHTMX(r) {
		return h.renderActorsFragment(w, r, production, ActorFormView{})
	}
	Redirect(w, r, productionActorsPath(production.ID), http.StatusSeeOther)
	return nil
}

// ArchiveActor archives rather than deleting an actor. It accepts the actor ID
// from a mux path value, query parameter, or form field.
func (h *ActorHandler) ArchiveActor(w http.ResponseWriter, r *http.Request) error {
	production, err := h.productionForRequest(r)
	if err != nil {
		return err
	}
	if production == nil {
		return &Error{Status: http.StatusNotFound, Message: "No active production exists.", Err: storage.ErrNotFound}
	}
	id, err := actorID(r)
	if err != nil {
		return &Error{Status: http.StatusBadRequest, Message: "Choose a valid actor.", Err: err}
	}
	actor, err := h.actors.Get(r.Context(), production.ID, id)
	if err != nil {
		return h.repositoryError("get actor", err)
	}
	if actor.ProductionID != production.ID {
		return &Error{Status: http.StatusNotFound, Message: "Actor not found.", Err: storage.ErrNotFound}
	}
	if err := h.actors.Archive(r.Context(), production.ID, id); err != nil {
		return h.repositoryError("archive actor", err)
	}
	SetTrigger(w, "actor:archived")
	if IsHTMX(r) {
		return h.renderActorsFragment(w, r, production, ActorFormView{})
	}
	Redirect(w, r, productionActorsPath(production.ID), http.StatusSeeOther)
	return nil
}

// Production and Actors are concise aliases suitable for mux method values.
func (h *ActorHandler) Production(w http.ResponseWriter, r *http.Request) error {
	if r.Method == http.MethodPost {
		return h.CreateProduction(w, r)
	}
	return h.ProductionBootstrap(w, r)
}

func (h *ActorHandler) Actors(w http.ResponseWriter, r *http.Request) error {
	if r.Method == http.MethodPost {
		return h.CreateActor(w, r)
	}
	return h.ListActors(w, r)
}

func (h *ActorHandler) activeProduction(ctx context.Context) (*storage.Production, error) {
	if h.productions == nil {
		return nil, &Error{Status: http.StatusInternalServerError, Message: "Production storage is unavailable.", Err: errors.New("web: production repository is required")}
	}
	productions, err := h.productions.List(ctx)
	if err != nil {
		return nil, h.repositoryError("list productions", err)
	}
	active := make([]storage.Production, 0, len(productions))
	for _, production := range productions {
		if production.ArchivedAt == nil {
			active = append(active, production)
		}
	}
	sort.SliceStable(active, func(i, j int) bool {
		if active[i].Name == active[j].Name {
			return active[i].ID < active[j].ID
		}
		return active[i].Name < active[j].Name
	})
	if len(active) == 0 {
		return nil, nil
	}
	result := active[0]
	return &result, nil
}

func (h *ActorHandler) productionForRequest(r *http.Request) (*storage.Production, error) {
	value := r.PathValue("production")
	if value == "" {
		return h.activeProduction(r.Context())
	}
	if h.productions == nil {
		return nil, &Error{Status: http.StatusInternalServerError, Message: "Production storage is unavailable.", Err: errors.New("web: production repository is required")}
	}
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id < 1 {
		return nil, &Error{Status: http.StatusBadRequest, Message: "Choose a valid production.", Err: errors.New("production id must be a positive integer")}
	}
	production, err := h.productions.Get(r.Context(), id)
	if err != nil {
		return nil, h.repositoryError("get production", err)
	}
	if production.ArchivedAt != nil {
		return nil, h.repositoryError("get production", storage.ErrArchived)
	}
	return &production, nil
}

func productionActorsPath(productionID int64) string {
	return "/production/" + strconv.FormatInt(productionID, 10) + "/actors"
}

func (h *ActorHandler) renderActors(w http.ResponseWriter, r *http.Request, production *storage.Production, form ActorFormView) error {
	if h.actors == nil {
		return &Error{Status: http.StatusInternalServerError, Message: "Actor storage is unavailable.", Err: errors.New("web: actor repository is required")}
	}
	actors, err := h.actors.List(r.Context(), production.ID, true)
	if err != nil {
		return h.repositoryError("list actors", err)
	}
	model := h.modelForProduction(production, form)
	for _, actor := range actors {
		view := ActorView{ID: actor.ID, Name: actor.Name, Role: actor.Role, Notes: actor.Notes, Archived: actor.ArchivedAt != nil, Production: actor.ProductionID}
		if view.Archived {
			model.ArchivedActors = append(model.ArchivedActors, view)
		} else {
			model.Actors = append(model.Actors, view)
		}
	}
	model.Empty = len(model.Actors) == 0
	return h.renderModel(w, r, http.StatusOK, "actor-page", "actor-list", model)
}

func (h *ActorHandler) renderActorsFragment(w http.ResponseWriter, r *http.Request, production *storage.Production, form ActorFormView) error {
	return h.renderActors(w, r, production, form)
}

func (h *ActorHandler) modelForProduction(production *storage.Production, form ActorFormView) ActorPageModel {
	return ActorPageModel{Title: production.Name + " actors", Production: &ProductionView{ID: production.ID, Name: production.Name}, ActorForm: form}
}

func (h *ActorHandler) render(w http.ResponseWriter, r *http.Request, status int, fullName, fragmentName string, model ActorPageModel) error {
	if IsHTMX(r) {
		return h.renderModel(w, r, status, fullName, fragmentName, model)
	}
	return h.renderModel(w, r, status, fullName, fullName, model)
}

func (h *ActorHandler) renderModel(w http.ResponseWriter, r *http.Request, status int, fullName, fragmentName string, model ActorPageModel) error {
	if h.pages == nil {
		return &Error{Status: http.StatusInternalServerError, Message: "Unable to render the page.", Err: errors.New("web: template set is required")}
	}
	name := fullName
	if IsHTMX(r) {
		name = fragmentName
	}
	if name == "" {
		return &Error{Status: http.StatusInternalServerError, Message: "Unable to render the page.", Err: errors.New("web: template name is required")}
	}
	if IsHTMX(r) {
		return RenderFragment(w, h.pages, name, status, model)
	}
	var page bytes.Buffer
	if err := h.pages.ExecuteTemplate(&page, name, model); err != nil {
		return &Error{Status: http.StatusInternalServerError, Message: "Unable to render the page.", Err: fmt.Errorf("render %s: %w", name, err)}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, err := page.WriteTo(w)
	return err
}

func (h *ActorHandler) repositoryError(operation string, err error) error {
	status := http.StatusInternalServerError
	message := "Unable to complete that request."
	if errors.Is(err, storage.ErrNotFound) {
		status, message = http.StatusNotFound, "That record could not be found."
	}
	if errors.Is(err, storage.ErrArchived) {
		status, message = http.StatusConflict, "That record is archived."
	}
	return &Error{Status: status, Message: message, Err: fmt.Errorf("%s: %w", operation, err)}
}

func actorFormFromRequest(r *http.Request, editing bool, id int64) ActorFormView {
	return ActorFormView{ID: id, Name: r.FormValue("name"), Role: r.FormValue("role"), Notes: r.FormValue("notes"), Editing: editing}
}

func actorFormFromActor(actor storage.Actor) ActorFormView {
	return ActorFormView{ID: actor.ID, Name: actor.Name, Role: actor.Role, Notes: actor.Notes, Editing: true}
}
func actorID(r *http.Request) (int64, error) {
	value := r.PathValue("id")
	if value == "" {
		value = r.PathValue("actorID")
	}
	if value == "" {
		value = r.URL.Query().Get("id")
	}
	if value == "" {
		value = r.URL.Query().Get("actor_id")
	}
	if value == "" {
		value = r.FormValue("id")
	}
	if value == "" {
		value = r.FormValue("actor_id")
	}
	if value == "" {
		return 0, errors.New("actor id is required")
	}
	id, err := strconv.ParseInt(value, 10, 64)

	if err != nil || id < 1 {
		return 0, errors.New("actor id must be a positive integer")
	}
	return id, nil
}
func seedDefaultItemTypes(ctx context.Context, repository storage.ItemTypeRepository, productionID int64) error {
	existing, err := repository.List(ctx, productionID, true)
	if err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(existing))
	for _, item := range existing {
		seen[strings.ToLower(strings.TrimSpace(item.Name))] = struct{}{}
	}
	for _, name := range DefaultItemTypeNames {
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			continue
		}
		if _, err := repository.Create(ctx, storage.CreateItemTypeInput{ProductionID: productionID, Name: name}); err != nil {
			return err
		}
		seen[key] = struct{}{}
	}
	return nil
}
