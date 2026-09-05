package web

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"costume-tree/internal/storage"
)

//go:embed templates/costume_items.html
var costumeItemTemplates embed.FS

var costumeItemStatuses = []string{
	storage.StatusNotStarted,
	storage.StatusInProgress,
	storage.StatusBlocked,
	storage.StatusReady,
	storage.StatusComplete,
}

type CostumeItemView struct {
	ID          int64
	Production  int64
	ActorID     int64
	ActorName   string
	ItemTypeID  int64
	ItemType    string
	Code        string
	Description string
	Status      string
	Progress    int
	NextAction  string
	Blocker     string
	Notes       string
	Archived    bool
	UpdatedAt   string
	Warning     string
}

type CostumeItemFormView struct {
	ID          int64
	ActorID     int64
	ItemTypeID  int64
	Description string
	Status      string
	Progress    string
	NextAction  string
	Blocker     string
	Notes       string
	UpdatedAt   string
	Editing     bool
	Warning     string
	Errors      FieldErrors
}

type CostumeItemPageModel struct {
	Title      string
	Production storage.Production
	Actor      storage.Actor
	Actors     []storage.Actor
	ItemTypes  []storage.ItemType
	Items      []CostumeItemView
	Archived   []CostumeItemView
	Item       *CostumeItemView
	Form       CostumeItemFormView
	Statuses   []string
	Error      string
	Success    string
	Lookup     string
	Empty      bool
}

// CostumeItemHandler owns production- and actor-scoped costume item HTTP behavior.
type CostumeItemHandler struct {
	productions storage.ProductionRepository
	actors      storage.ActorRepository
	itemTypes   storage.ItemTypeRepository
	items       storage.CostumeItemRepository
	pages       *template.Template
}

// NewCostumeItemHandler constructs a handler from production-scoped repositories.
// A supplied template set is extended with the costume item templates.
func NewCostumeItemHandler(productions storage.ProductionRepository, actors storage.ActorRepository, itemTypes storage.ItemTypeRepository, items storage.CostumeItemRepository, pages ...*template.Template) *CostumeItemHandler {
	parsed := mustCostumeItemTemplates()
	if len(pages) > 0 && pages[0] != nil {
		parsed = pages[0]
		if _, err := parsed.ParseFS(costumeItemTemplates, "templates/costume_items.html"); err != nil {
			parsed = mustCostumeItemTemplates()
		}
	}
	return &CostumeItemHandler{productions: productions, actors: actors, itemTypes: itemTypes, items: items, pages: parsed}
}

func mustCostumeItemTemplates() *template.Template {
	parsed, err := template.ParseFS(costumeItemTemplates, "templates/costume_items.html")
	if err != nil {
		panic(fmt.Sprintf("web: parse costume item templates: %v", err))
	}
	return parsed
}

// ListCostumeItems renders the active items for an actor. Include archived
// records in the page only when requested; ordinary lists remain active-only.
func (h *CostumeItemHandler) ListCostumeItems(w http.ResponseWriter, r *http.Request) error {
	productionID, actorID, err := costumeItemScopeIDs(r)
	if err != nil {
		return err
	}
	if r.Method != http.MethodGet {
		return costumeItemMethodError("costume item list requires GET")
	}
	production, actor, err := h.scope(r.Context(), productionID, actorID)
	if err != nil {
		return err
	}
	items, err := h.items.List(r.Context(), storage.CostumeItemFilter{ProductionID: productionID, ActorID: actorID})
	if err != nil {
		return h.storageError("list costume items", err)
	}
	model := h.listModel(r.Context(), production, actor, items)
	return h.render(w, r, http.StatusOK, "costume-items-page", "costume-items-list", model)
}

// CreateCostumeItem validates and creates an item in the scoped actor.
func (h *CostumeItemHandler) CreateCostumeItem(w http.ResponseWriter, r *http.Request) error {
	productionID, actorID, err := costumeItemScopeIDs(r)
	if err != nil {
		return err
	}
	if r.Method != http.MethodPost {
		return costumeItemMethodError("costume item create requires POST")
	}
	production, actor, err := h.scope(r.Context(), productionID, actorID)
	if err != nil {
		return err
	}
	if err := r.ParseForm(); err != nil {
		return &Error{Status: http.StatusBadRequest, Message: "Unable to read the costume item form.", Err: fmt.Errorf("parse costume item form: %w", err)}
	}
	form := costumeItemFormFromRequest(r, false, 0)
	model, input, valid := h.validateForm(r.Context(), production, actor, form)
	if !valid {
		return h.renderFormError(w, r, http.StatusUnprocessableEntity, model)
	}
	item, err := h.items.Create(r.Context(), input)
	if err != nil {
		return h.storageError("create costume item", err)
	}
	SetTrigger(w, "costume-item:created")
	if IsHTMX(r) {
		items, listErr := h.items.List(r.Context(), storage.CostumeItemFilter{ProductionID: productionID, ActorID: actorID})
		if listErr != nil {
			return h.storageError("list costume items", listErr)
		}
		model := h.listModel(r.Context(), production, actor, items)
		model.Success = "Created " + item.Code + "."
		return RenderFragment(w, h.pages, "costume-items-list", http.StatusOK, model)
	}
	Redirect(w, r, costumeItemListPath(productionID, actorID), http.StatusSeeOther)
	return nil
}

// DetailCostumeItem renders a single item. Archived records remain readable.
func (h *CostumeItemHandler) DetailCostumeItem(w http.ResponseWriter, r *http.Request) error {
	productionID, actorID, itemID, err := costumeItemIDs(r)
	if err != nil {
		return err
	}
	if r.Method != http.MethodGet {
		return costumeItemMethodError("costume item detail requires GET")
	}
	production, actor, err := h.scope(r.Context(), productionID, actorID)
	if err != nil {
		return err
	}
	item, err := h.items.Get(r.Context(), productionID, itemID)
	if err != nil {
		return h.storageError("get costume item", err)
	}
	if item.ActorID != actorID || item.ProductionID != productionID {
		return costumeItemNotFound()
	}
	view := h.view(r.Context(), productionID, item)
	model := CostumeItemPageModel{Title: item.Code + " · " + production.Name, Production: production, Actor: actor, Item: &view, Statuses: costumeItemStatuses}
	if IsHTMX(r) {
		return RenderFragment(w, h.pages, "costume-item-detail", http.StatusOK, model)
	}
	return h.render(w, r, http.StatusOK, "costume-item-page", "costume-item-detail", model)
}

// EditCostumeItem serves an edit form and updates the item on POST.
func (h *CostumeItemHandler) EditCostumeItem(w http.ResponseWriter, r *http.Request) error {
	productionID, actorID, itemID, err := costumeItemIDs(r)
	if err != nil {
		return err
	}
	production, actor, err := h.scope(r.Context(), productionID, actorID)
	if err != nil {
		return err
	}
	item, err := h.items.Get(r.Context(), productionID, itemID)
	if err != nil {
		return h.storageError("get costume item", err)
	}
	if item.ActorID != actorID || item.ProductionID != productionID {
		return costumeItemNotFound()
	}
	if item.ArchivedAt != nil {
		return h.storageError("edit costume item", storage.ErrArchived)
	}
	if r.Method == http.MethodGet {
		return h.renderEdit(w, r, http.StatusOK, production, actor, item, costumeItemFormFromItem(item))
	}
	if r.Method != http.MethodPost {
		return costumeItemMethodError("costume item edit requires GET or POST")
	}
	if err := r.ParseForm(); err != nil {
		return &Error{Status: http.StatusBadRequest, Message: "Unable to read the costume item form.", Err: fmt.Errorf("parse costume item form: %w", err)}
	}
	form := costumeItemFormFromRequest(r, true, item.ID)
	if form.UpdatedAt == "" {
		form.UpdatedAt = r.FormValue("updatedAt")
	}
	if form.UpdatedAt == "" || !sameTimestamp(form.UpdatedAt, item.UpdatedAt) {
		form.Errors = FieldErrors{"updated_at": "This item changed in another window. Reload before saving."}
		return h.renderEdit(w, r, http.StatusConflict, production, actor, item, form)
	}
	model, input, valid := h.validateForm(r.Context(), production, actor, form)
	if !valid {
		return h.renderFormError(w, r, http.StatusUnprocessableEntity, model)
	}
	update := storage.UpdateCostumeItemInput{
		ProductionID:      productionID,
		ID:                item.ID,
		ActorID:           actor.ID,
		ItemTypeID:        input.ItemTypeID,
		Description:       input.Description,
		Status:            input.Status,
		Progress:          input.Progress,
		NextAction:        input.NextAction,
		Blocker:           input.Blocker,
		Notes:             input.Notes,
		ExpectedUpdatedAt: &item.UpdatedAt,
	}
	updated, err := h.items.Update(r.Context(), update)
	if err != nil {
		return h.storageError("update costume item", err)
	}
	SetTrigger(w, "costume-item:updated")
	if IsHTMX(r) {
		view := h.view(r.Context(), productionID, updated)
		model := CostumeItemPageModel{Title: updated.Code + " · " + production.Name, Production: production, Actor: actor, Item: &view, Form: costumeItemFormFromItem(updated), Statuses: costumeItemStatuses}
		return RenderFragment(w, h.pages, "costume-item-detail", http.StatusOK, model)
	}
	Redirect(w, r, costumeItemDetailPath(productionID, actorID, item.ID), http.StatusSeeOther)
	return nil
}

// ArchiveCostumeItem archives an item. Its detail view remains accessible.
func (h *CostumeItemHandler) ArchiveCostumeItem(w http.ResponseWriter, r *http.Request) error {
	productionID, actorID, itemID, err := costumeItemIDs(r)
	if err != nil {
		return err
	}
	if r.Method != http.MethodPost {
		return costumeItemMethodError("costume item archive requires POST")
	}
	production, actor, err := h.scope(r.Context(), productionID, actorID)
	if err != nil {
		return err
	}
	item, err := h.items.Get(r.Context(), productionID, itemID)
	if err != nil {
		return h.storageError("get costume item", err)
	}
	if item.ActorID != actorID {
		return costumeItemNotFound()
	}
	if err := h.items.Archive(r.Context(), productionID, itemID); err != nil {
		return h.storageError("archive costume item", err)
	}
	SetTrigger(w, "costume-item:archived")
	if IsHTMX(r) {
		items, listErr := h.items.List(r.Context(), storage.CostumeItemFilter{ProductionID: productionID, ActorID: actorID})
		if listErr != nil {
			return h.storageError("list costume items", listErr)
		}
		model := h.listModel(r.Context(), production, actor, items)
		model.Success = item.Code + " archived."
		return RenderFragment(w, h.pages, "costume-items-list", http.StatusOK, model)
	}
	Redirect(w, r, costumeItemListPath(productionID, actorID), http.StatusSeeOther)
	return nil
}

// LookupCostumeItem performs an exact, production-scoped code lookup.
func (h *CostumeItemHandler) LookupCostumeItem(w http.ResponseWriter, r *http.Request) error {
	productionID, err := costumeProductionID(r)
	if err != nil {
		return err
	}
	code := r.PathValue("code")
	if code == "" {
		code = r.URL.Query().Get("code")
	}
	if code == "" {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		for i, part := range parts {
			if (part == "code" || part == "codes") && i+1 < len(parts) {
				code, _ = url.PathUnescape(parts[i+1])
				break
			}
			if (part == "items" || part == "item") && i+1 < len(parts) && strings.HasPrefix(parts[i+1], "C-") {
				code, _ = url.PathUnescape(parts[i+1])
				break
			}
		}
	}
	code = strings.TrimSpace(code)
	if code == "" {
		return &Error{Status: http.StatusBadRequest, Message: "Enter a costume item code.", Err: errors.New("web: missing costume item code")}
	}
	if r.Method != http.MethodGet {
		return costumeItemMethodError("costume item lookup requires GET")
	}
	matches, err := h.items.List(r.Context(), storage.CostumeItemFilter{ProductionID: productionID, Code: code, IncludeArchived: true})
	if err != nil {
		return h.storageError("lookup costume item", err)
	}
	if len(matches) != 1 {
		return h.storageError("lookup costume item", storage.ErrNotFound)
	}
	item := matches[0]
	actor, err := h.actors.Get(r.Context(), productionID, item.ActorID)
	if err != nil {
		return h.storageError("get costume item actor", err)
	}
	production, err := h.productions.Get(r.Context(), productionID)
	if err != nil {
		return h.storageError("get production", err)
	}
	view := h.view(r.Context(), productionID, item)
	model := CostumeItemPageModel{Title: item.Code + " · " + production.Name, Production: production, Actor: actor, Item: &view, Statuses: costumeItemStatuses, Lookup: code}
	if IsHTMX(r) {
		return RenderFragment(w, h.pages, "costume-item-detail", http.StatusOK, model)
	}
	return h.render(w, r, http.StatusOK, "costume-item-page", "costume-item-detail", model)
}

// Conventional aliases for mux method values.
func (h *CostumeItemHandler) CostumeItems(w http.ResponseWriter, r *http.Request) error {
	if r.Method == http.MethodPost {
		return h.CreateCostumeItem(w, r)
	}
	return h.ListCostumeItems(w, r)
}
func (h *CostumeItemHandler) CostumeItem(w http.ResponseWriter, r *http.Request) error {
	if r.Method == http.MethodPost {
		return h.EditCostumeItem(w, r)
	}
	return h.DetailCostumeItem(w, r)
}
func (h *CostumeItemHandler) CreateItem(w http.ResponseWriter, r *http.Request) error {
	return h.CreateCostumeItem(w, r)
}
func (h *CostumeItemHandler) EditItem(w http.ResponseWriter, r *http.Request) error {
	return h.EditCostumeItem(w, r)
}
func (h *CostumeItemHandler) ArchiveItem(w http.ResponseWriter, r *http.Request) error {
	return h.ArchiveCostumeItem(w, r)
}
func (h *CostumeItemHandler) LookupCode(w http.ResponseWriter, r *http.Request) error {
	return h.LookupCostumeItem(w, r)
}
func (h *CostumeItemHandler) LookupCostumeItemByCode(w http.ResponseWriter, r *http.Request) error {
	return h.LookupCostumeItem(w, r)
}

func (h *CostumeItemHandler) scope(ctx context.Context, productionID, actorID int64) (storage.Production, storage.Actor, error) {
	if h.productions == nil || h.actors == nil || h.itemTypes == nil || h.items == nil {
		return storage.Production{}, storage.Actor{}, &Error{Status: http.StatusInternalServerError, Message: "Costume item storage is unavailable.", Err: errors.New("web: costume item repositories are required")}
	}
	production, err := h.productions.Get(ctx, productionID)
	if err != nil {
		return storage.Production{}, storage.Actor{}, h.storageError("get production", err)
	}
	if production.ArchivedAt != nil {
		return storage.Production{}, storage.Actor{}, &Error{Status: http.StatusConflict, Message: "That production is archived.", Err: storage.ErrArchived}
	}
	actor, err := h.actors.Get(ctx, productionID, actorID)
	if err != nil {
		return storage.Production{}, storage.Actor{}, h.storageError("get actor", err)
	}
	if actor.ArchivedAt != nil {
		return storage.Production{}, storage.Actor{}, &Error{Status: http.StatusConflict, Message: "That actor is archived.", Err: storage.ErrArchived}
	}
	return production, actor, nil
}

func (h *CostumeItemHandler) validateForm(ctx context.Context, production storage.Production, actor storage.Actor, form CostumeItemFormView) (CostumeItemPageModel, storage.CreateCostumeItemInput, bool) {
	model := CostumeItemPageModel{Title: "Costume items · " + production.Name, Production: production, Actor: actor, Form: form, Statuses: costumeItemStatuses}
	input := storage.CreateCostumeItemInput{ProductionID: production.ID, ActorID: actor.ID, ItemTypeID: form.ItemTypeID, Description: strings.TrimSpace(form.Description), Status: strings.TrimSpace(form.Status), NextAction: strings.TrimSpace(form.NextAction), Blocker: strings.TrimSpace(form.Blocker), Notes: form.Notes}
	if form.ActorID != 0 && form.ActorID != actor.ID {
		model.Form.Errors = addFieldError(model.Form.Errors, "actor_id", "Choose the actor in this production.")
	}
	if input.Status == "" {
		input.Status = storage.StatusNotStarted
		model.Form.Status = input.Status
	}
	progressText := strings.TrimSpace(form.Progress)
	if progressText == "" {
		progressText = "0"
		model.Form.Progress = "0"
	}
	if !containsStatus(input.Status) {
		model.Form.Errors = addFieldError(model.Form.Errors, "status", "Choose a valid status.")
	}
	progress, err := strconv.Atoi(progressText)
	if err != nil || progress < 0 || progress > 100 {
		model.Form.Errors = addFieldError(model.Form.Errors, "progress", "Progress must be a whole number from 0 to 100.")
	} else {
		input.Progress = progress
	}
	if input.Status == storage.StatusComplete && input.Progress != 100 {
		model.Form.Errors = addFieldError(model.Form.Errors, "progress", "Complete items must be at 100%.")
	}
	if input.ItemTypeID <= 0 {
		model.Form.Errors = addFieldError(model.Form.Errors, "item_type_id", "Choose an item type.")
	} else if h.itemTypes != nil {
		typ, typeErr := h.itemTypes.Get(ctx, production.ID, input.ItemTypeID)
		if typeErr != nil || typ.ArchivedAt != nil {
			model.Form.Errors = addFieldError(model.Form.Errors, "item_type_id", "Choose an active item type from this production.")
		}
	}
	if input.Blocker != "" && input.Status != storage.StatusBlocked {
		model.Form.Warning = "This blocker is saved, but the item is not marked Blocked."
	}
	model.Form = formWithValidatedStatus(model.Form, input.Status)
	return model, input, len(model.Form.Errors) == 0
}

func formWithValidatedStatus(form CostumeItemFormView, status string) CostumeItemFormView {
	if form.Status == "" {
		form.Status = status
	}
	return form
}
func addFieldError(current FieldErrors, field, message string) FieldErrors {
	if current == nil {
		current = FieldErrors{}
	}
	current[field] = message
	return current
}
func containsStatus(status string) bool {
	for _, allowed := range costumeItemStatuses {
		if status == allowed {
			return true
		}
	}
	return false
}

func (h *CostumeItemHandler) listModel(ctx context.Context, production storage.Production, actor storage.Actor, items []storage.CostumeItem) CostumeItemPageModel {
	model := CostumeItemPageModel{Title: "Costume items · " + actor.Name, Production: production, Actor: actor, Statuses: costumeItemStatuses}
	model.Actors, model.ItemTypes = h.selectors(ctx, production.ID)
	for _, item := range items {
		model.Items = append(model.Items, h.view(ctx, production.ID, item))
	}
	model.Empty = len(model.Items) == 0
	return model
}

func (h *CostumeItemHandler) view(ctx context.Context, productionID int64, item storage.CostumeItem) CostumeItemView {
	view := CostumeItemView{ID: item.ID, Production: item.ProductionID, ActorID: item.ActorID, ItemTypeID: item.ItemTypeID, Code: item.Code, Description: item.Description, Status: item.Status, Progress: item.Progress, NextAction: item.NextAction, Blocker: item.Blocker, Notes: item.Notes, Archived: item.ArchivedAt != nil, UpdatedAt: formatUpdatedAt(item.UpdatedAt)}
	if view.Blocker != "" && view.Status != storage.StatusBlocked {
		view.Warning = "Blocker noted while status is not Blocked."
	}
	if h.actors != nil {
		if actor, err := h.actors.Get(ctx, productionID, item.ActorID); err == nil {
			view.ActorName = actor.Name
		}
	}
	if h.itemTypes != nil {
		if typ, err := h.itemTypes.Get(ctx, productionID, item.ItemTypeID); err == nil {
			view.ItemType = typ.Name
		}
	}
	return view
}

func costumeItemFormFromRequest(r *http.Request, editing bool, id int64) CostumeItemFormView {
	return CostumeItemFormView{ID: id, ActorID: parseFormInt(r.FormValue("actor_id")), ItemTypeID: parseFormInt(r.FormValue("item_type_id")), Description: r.FormValue("description"), Status: r.FormValue("status"), Progress: r.FormValue("progress"), NextAction: r.FormValue("next_action"), Blocker: r.FormValue("blocker"), Notes: r.FormValue("notes"), UpdatedAt: r.FormValue("updated_at"), Editing: editing}
}
func costumeItemFormFromItem(item storage.CostumeItem) CostumeItemFormView {
	return CostumeItemFormView{ID: item.ID, ActorID: item.ActorID, ItemTypeID: item.ItemTypeID, Description: item.Description, Status: item.Status, Progress: strconv.Itoa(item.Progress), NextAction: item.NextAction, Blocker: item.Blocker, Notes: item.Notes, UpdatedAt: formatUpdatedAt(item.UpdatedAt), Editing: true}
}
func parseFormInt(value string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	return n
}

func (h *CostumeItemHandler) renderEdit(w http.ResponseWriter, r *http.Request, status int, production storage.Production, actor storage.Actor, item storage.CostumeItem, form CostumeItemFormView) error {
	model := CostumeItemPageModel{Title: "Edit " + item.Code, Production: production, Actor: actor, Item: func() *CostumeItemView { v := h.view(r.Context(), production.ID, item); return &v }(), Form: form, Statuses: costumeItemStatuses}
	model.Actors, model.ItemTypes = h.selectors(r.Context(), production.ID)
	return h.render(w, r, status, "costume-item-page", "costume-item-form", model)
}
func (h *CostumeItemHandler) selectors(ctx context.Context, productionID int64) ([]storage.Actor, []storage.ItemType) {
	actors, _ := h.actors.List(ctx, productionID)
	types, _ := h.itemTypes.List(ctx, productionID)
	return actors, types
}
func (h *CostumeItemHandler) renderFormError(w http.ResponseWriter, r *http.Request, status int, model CostumeItemPageModel) error {
	model.Actors, model.ItemTypes = h.selectors(r.Context(), model.Production.ID)
	if IsHTMX(r) {
		SetTrigger(w, "costume-item:error")
		return RenderFragment(w, h.pages, "costume-item-form", status, model)
	}
	if !model.Form.Editing {
		items, err := h.items.List(r.Context(), storage.CostumeItemFilter{ProductionID: model.Production.ID, ActorID: model.Actor.ID})
		if err != nil {
			return h.storageError("list costume items", err)
		}
		model.Items = make([]CostumeItemView, 0, len(items))
		for _, item := range items {
			model.Items = append(model.Items, h.view(r.Context(), model.Production.ID, item))
		}
		model.Empty = len(model.Items) == 0
		return h.render(w, r, status, "costume-items-page", "costume-item-form", model)
	}
	return h.render(w, r, status, "costume-item-page", "costume-item-form", model)
}
func (h *CostumeItemHandler) render(w http.ResponseWriter, r *http.Request, status int, fullName, fragmentName string, model CostumeItemPageModel) error {
	if h.pages == nil {
		return &Error{Status: http.StatusInternalServerError, Message: "Unable to render the page.", Err: errors.New("web: template set is required")}
	}
	name := fullName
	if IsHTMX(r) {
		return RenderFragment(w, h.pages, fragmentName, status, model)
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

func (h *CostumeItemHandler) storageError(operation string, err error) error {
	status, message := http.StatusInternalServerError, "Unable to update costume items."
	if errors.Is(err, storage.ErrNotFound) {
		status, message = http.StatusNotFound, "That costume item, actor, item type, or production was not found."
	}
	if errors.Is(err, storage.ErrArchived) {
		status, message = http.StatusConflict, "That record is archived."
	}
	return &Error{Status: status, Message: message, Err: fmt.Errorf("%s: %w", operation, err)}
}
func costumeItemNotFound() error {
	return &Error{Status: http.StatusNotFound, Message: "Costume item not found.", Err: storage.ErrNotFound}
}
func costumeItemMethodError(message string) error {
	return &Error{Status: http.StatusMethodNotAllowed, Message: "Method not allowed.", Err: errors.New("web: " + message)}
}

func formatUpdatedAt(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }
func sameTimestamp(value string, expected time.Time) bool {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && parsed.Equal(expected)
}

func costumeProductionID(r *http.Request) (int64, error) {
	values := []string{r.PathValue("production"), r.URL.Query().Get("production_id")}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	for i, part := range parts {
		if part == "production" && i+1 < len(parts) {
			values = append(values, parts[i+1])
			break
		}
	}
	for _, value := range values {
		if value == "" {
			continue
		}
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil || id <= 0 {
			return 0, &Error{Status: http.StatusBadRequest, Message: "Choose a valid production.", Err: fmt.Errorf("web: invalid production id %q", value)}
		}
		return id, nil
	}
	return 0, &Error{Status: http.StatusBadRequest, Message: "A production is required.", Err: errors.New("web: missing production id")}
}
func costumeActorID(r *http.Request) (int64, error) {
	values := []string{r.PathValue("actor"), r.URL.Query().Get("actor_id")}
	if value := r.FormValue("actor_id"); value != "" {
		values = append(values, value)
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	for i, part := range parts {
		if (part == "actors" || part == "actor") && i+1 < len(parts) {
			values = append(values, parts[i+1])
			break
		}
	}
	for _, value := range values {
		if value == "" {
			continue
		}
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil || id <= 0 {
			return 0, &Error{Status: http.StatusBadRequest, Message: "Choose a valid actor.", Err: fmt.Errorf("web: invalid actor id %q", value)}
		}
		return id, nil
	}
	return 0, &Error{Status: http.StatusBadRequest, Message: "An actor is required.", Err: errors.New("web: missing actor id")}
}
func costumeItemID(r *http.Request) (int64, error) {
	values := []string{r.PathValue("item"), r.PathValue("id"), r.URL.Query().Get("item_id"), r.URL.Query().Get("id")}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	for i, part := range parts {
		if (part == "items" || part == "item") && i+1 < len(parts) {
			if id, err := strconv.ParseInt(parts[i+1], 10, 64); err == nil && id > 0 {
				values = append(values, parts[i+1])
			}
			break
		}
	}
	for _, value := range values {
		if value == "" {
			continue
		}
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil || id <= 0 {
			return 0, &Error{Status: http.StatusBadRequest, Message: "Choose a valid costume item.", Err: fmt.Errorf("web: invalid item id %q", value)}
		}
		return id, nil
	}
	return 0, &Error{Status: http.StatusBadRequest, Message: "A costume item is required.", Err: errors.New("web: missing item id")}
}
func costumeItemScopeIDs(r *http.Request) (int64, int64, error) {
	p, err := costumeProductionID(r)
	if err != nil {
		return 0, 0, err
	}
	a, err := costumeActorID(r)
	return p, a, err
}
func costumeItemIDs(r *http.Request) (int64, int64, int64, error) {
	p, a, err := costumeItemScopeIDs(r)
	if err != nil {
		return 0, 0, 0, err
	}
	i, err := costumeItemID(r)
	return p, a, i, err
}
func costumeItemListPath(productionID, actorID int64) string {
	return "/production/" + strconv.FormatInt(productionID, 10) + "/actors/" + strconv.FormatInt(actorID, 10) + "/items"
}
func costumeItemDetailPath(productionID, actorID, itemID int64) string {
	return costumeItemListPath(productionID, actorID) + "/" + strconv.FormatInt(itemID, 10)
}
