package web

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"mime"
	"mime/multipart"
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
	storage.StatusFind,
	storage.StatusMake,
	storage.StatusFit,
	storage.StatusAlterations,
	storage.StatusComplete,
}

const costumeItemMultipartMemory = 8 << 20

// PhotoService validates, stores, lists, and resolves costume item photos.
// Upload persists the original before returning; derivative processing may
// continue after the request has redirected to the item detail page.
type PhotoService interface {
	Validate(*multipart.FileHeader) error
	Upload(context.Context, storage.CostumeItem, *multipart.FileHeader) (storage.CostumeItemPhoto, error)
	List(context.Context, int64, int64) ([]storage.CostumeItemPhoto, error)
	ListFirstReadyByActor(context.Context, int64, int64) ([]storage.CostumeItemPhoto, error)
	Resolve(context.Context, int64, int64, int64, string) (storage.CostumeItemPhoto, string, error)
}

type CostumeItemView struct {
	ID           int64
	Production   int64
	ActorID      int64
	ActorName    string
	ItemTypeID   int64
	ItemType     string
	Code         string
	Description  string
	Status       string
	Progress     int
	NextAction   string
	Blocker      string
	Notes        string
	Archived     bool
	UpdatedAt    string
	ThumbnailURL string
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
	Errors      FieldErrors
}

type CostumeItemPhotoView struct {
	storage.CostumeItemPhoto
	OriginalURL  string
	DisplayURL   string
	ThumbnailURL string
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
	Photos     []CostumeItemPhotoView
	HasPending bool
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
	photos      PhotoService
}

// NewCostumeItemHandler constructs a handler from production-scoped
// repositories, an explicit template set, and the photo service.
func NewCostumeItemHandler(productions storage.ProductionRepository, actors storage.ActorRepository, itemTypes storage.ItemTypeRepository, items storage.CostumeItemRepository, pages *template.Template, photos PhotoService) *CostumeItemHandler {
	if pages == nil {
		pages = mustCostumeItemTemplates()
	}
	return &CostumeItemHandler{
		productions: productions,
		actors:      actors,
		itemTypes:   itemTypes,
		items:       items,
		pages:       pages,
		photos:      photos,
	}
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
	model, err := h.listModel(r.Context(), production, actor, items)
	if err != nil {
		return err
	}
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
	photo, err := parseCostumeItemRequest(r)
	if err != nil {
		return err
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	form := costumeItemFormFromRequest(r, false, 0)
	model, input, valid := h.validateForm(r.Context(), production, actor, form)
	if photo != nil {
		if h.photos == nil {
			return photoServiceUnavailable()
		}
		if err := h.photos.Validate(photo); err != nil {
			model.Form.Errors = addFieldError(model.Form.Errors, "photo", "Choose a valid JPEG, PNG, or GIF photo.")
			valid = false
		}
	}
	if !valid {
		return h.renderFormError(w, r, http.StatusUnprocessableEntity, model)
	}
	item, err := h.items.Create(r.Context(), input)
	if err != nil {
		return h.storageError("create costume item", err)
	}
	detailPath := costumeItemDetailPath(productionID, actorID, item.ID)
	if photo != nil {
		if _, err := h.photos.Upload(r.Context(), item, photo); err != nil {
			detailPath += "?photo=failed"
		}
	}
	SetTrigger(w, "costume-item:created")
	if photo != nil {
		Redirect(w, r, detailPath, http.StatusSeeOther)
		return nil
	}
	if IsHTMX(r) {
		items, listErr := h.items.List(r.Context(), storage.CostumeItemFilter{ProductionID: productionID, ActorID: actorID})
		if listErr != nil {
			return h.storageError("list costume items", listErr)
		}
		model, err := h.listModel(r.Context(), production, actor, items)
		if err != nil {
			return err
		}
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
	production, actor, item, err := h.scopedItem(r.Context(), productionID, actorID, itemID)
	if err != nil {
		return err
	}
	model, err := h.detailModel(r.Context(), production, actor, item)
	if err != nil {
		return err
	}
	if r.URL.Query().Get("photo") == "failed" {
		model.Error = "Costume item saved, but the photo could not be attached. Choose it again in Edit item to retry."
	}
	if IsHTMX(r) {
		return RenderFragment(w, h.pages, "costume-item-detail", http.StatusOK, model)
	}
	return h.render(w, r, http.StatusOK, "costume-item-page", "costume-item-detail", model)
}

// UpdateCostumeItemStatus changes only an item's workflow status from the
// actor's inventory list.
func (h *CostumeItemHandler) UpdateCostumeItemStatus(w http.ResponseWriter, r *http.Request) error {
	productionID, actorID, itemID, err := costumeItemIDs(r)
	if err != nil {
		return err
	}
	if r.Method != http.MethodPost {
		return costumeItemMethodError("costume item status update requires POST")
	}
	production, actor, item, err := h.scopedItem(r.Context(), productionID, actorID, itemID)
	if err != nil {
		return err
	}
	if item.ArchivedAt != nil {
		return h.storageError("update costume item status", storage.ErrArchived)
	}
	if err := r.ParseForm(); err != nil {
		return &Error{Status: http.StatusBadRequest, Message: "Unable to read the costume item status.", Err: fmt.Errorf("parse costume item status: %w", err)}
	}
	status := strings.TrimSpace(r.FormValue("status"))
	if !containsStatus(status) {
		return h.statusUpdateError(w, r, production, actor, http.StatusUnprocessableEntity, "Choose a valid status.")
	}
	updatedAt := strings.TrimSpace(r.FormValue("updated_at"))
	if updatedAt == "" || !sameTimestamp(updatedAt, item.UpdatedAt) {
		return h.statusUpdateError(w, r, production, actor, http.StatusConflict, "This item changed in another window. Reload before saving.")
	}
	progress := item.Progress
	if status == storage.StatusComplete {
		progress = 100
	}
	updated, err := h.items.Update(r.Context(), storage.UpdateCostumeItemInput{
		ProductionID: productionID, ID: item.ID, ActorID: actor.ID, ItemTypeID: item.ItemTypeID,
		Description: item.Description, Status: status, Progress: progress, NextAction: item.NextAction,
		Blocker: item.Blocker, Notes: item.Notes, ExpectedUpdatedAt: &item.UpdatedAt,
	})
	if err != nil {
		return h.storageError("update costume item status", err)
	}
	SetTrigger(w, "costume-item:updated")
	if IsHTMX(r) {
		items, err := h.items.List(r.Context(), storage.CostumeItemFilter{ProductionID: productionID, ActorID: actorID})
		if err != nil {
			return h.storageError("list costume items", err)
		}
		model, err := h.listModel(r.Context(), production, actor, items)
		if err != nil {
			return err
		}
		model.Success = updated.Code + " updated."
		return RenderFragment(w, h.pages, "costume-items-list", http.StatusOK, model)
	}
	Redirect(w, r, costumeItemListPath(productionID, actorID), http.StatusSeeOther)
	return nil
}

func (h *CostumeItemHandler) statusUpdateError(w http.ResponseWriter, r *http.Request, production storage.Production, actor storage.Actor, status int, message string) error {
	if !IsHTMX(r) {
		return &Error{Status: status, Message: message, Err: errors.New("web: costume item status update rejected")}
	}
	items, err := h.items.List(r.Context(), storage.CostumeItemFilter{ProductionID: production.ID, ActorID: actor.ID})
	if err != nil {
		return h.storageError("list costume items", err)
	}
	model, err := h.listModel(r.Context(), production, actor, items)
	if err != nil {
		return err
	}
	model.Error = message
	return RenderFragment(w, h.pages, "costume-items-list", status, model)
}

// CostumeItemPhotoGallery returns the production-, actor-, and item-scoped
// gallery fragment used by the detail page's pending-photo poller.
func (h *CostumeItemHandler) CostumeItemPhotoGallery(w http.ResponseWriter, r *http.Request) error {
	productionID, actorID, itemID, err := costumeItemIDs(r)
	if err != nil {
		return err
	}
	if r.Method != http.MethodGet {
		return costumeItemMethodError("costume item photo gallery requires GET")
	}
	production, actor, item, err := h.scopedItem(r.Context(), productionID, actorID, itemID)
	if err != nil {
		return err
	}
	model, err := h.detailModel(r.Context(), production, actor, item)
	if err != nil {
		return err
	}
	return RenderFragment(w, h.pages, "costume-item-photo-gallery", http.StatusOK, model)
}

// ServeCostumeItemPhoto serves one immutable photo variant after verifying the
// complete production, actor, item, and photo scope.
func (h *CostumeItemHandler) ServeCostumeItemPhoto(w http.ResponseWriter, r *http.Request) error {
	productionID, actorID, itemID, err := costumeItemIDs(r)
	if err != nil {
		return err
	}
	if r.Method != http.MethodGet {
		return costumeItemMethodError("costume item photo requires GET")
	}
	if _, _, _, err := h.scopedItem(r.Context(), productionID, actorID, itemID); err != nil {
		return err
	}
	if h.photos == nil {
		return photoServiceUnavailable()
	}
	photoID, err := costumeItemPhotoID(r)
	if err != nil {
		return err
	}
	variant := costumeItemPhotoVariant(r)
	switch variant {
	case "original", "display", "thumbnail":
	default:
		return costumeItemNotFound()
	}
	photo, path, err := h.photos.Resolve(r.Context(), productionID, itemID, photoID, variant)
	if err != nil {
		return h.photoError("resolve costume item photo", err)
	}
	if photo.ProductionID != productionID || photo.CostumeItemID != itemID || photo.ID != photoID {
		return costumeItemNotFound()
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	if photo.MediaType != "" {
		w.Header().Set("Content-Type", photo.MediaType)
	}
	http.ServeFile(w, r, path)
	return nil
}

// EditCostumeItem serves an edit form and updates the item on POST.
func (h *CostumeItemHandler) EditCostumeItem(w http.ResponseWriter, r *http.Request) error {
	productionID, actorID, itemID, err := costumeItemIDs(r)
	if err != nil {
		return err
	}
	production, actor, item, err := h.scopedItem(r.Context(), productionID, actorID, itemID)
	if err != nil {
		return err
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
	photo, err := parseCostumeItemRequest(r)
	if err != nil {
		return err
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
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
	if photo != nil {
		if h.photos == nil {
			return photoServiceUnavailable()
		}
		if err := h.photos.Validate(photo); err != nil {
			model.Form.Errors = addFieldError(model.Form.Errors, "photo", "Choose a valid JPEG, PNG, or GIF photo.")
			valid = false
		}
	}
	if !valid {
		view := h.view(r.Context(), productionID, item)
		model.Item = &view
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
	detailPath := costumeItemDetailPath(productionID, actorID, item.ID)
	if photo != nil {
		if _, err := h.photos.Upload(r.Context(), updated, photo); err != nil {
			detailPath += "?photo=failed"
		}
	}
	SetTrigger(w, "costume-item:updated")
	if photo != nil {
		Redirect(w, r, detailPath, http.StatusSeeOther)
		return nil
	}
	if IsHTMX(r) {
		model, err := h.detailModel(r.Context(), production, actor, updated)
		if err != nil {
			return err
		}
		return RenderFragment(w, h.pages, "costume-item-detail", http.StatusOK, model)
	}
	Redirect(w, r, detailPath, http.StatusSeeOther)
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
		model, err := h.listModel(r.Context(), production, actor, items)
		if err != nil {
			return err
		}
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
	model, err := h.detailModel(r.Context(), production, actor, item)
	if err != nil {
		return err
	}
	model.Lookup = code
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
		input.Status = storage.StatusFind
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

func (h *CostumeItemHandler) listModel(ctx context.Context, production storage.Production, actor storage.Actor, items []storage.CostumeItem) (CostumeItemPageModel, error) {
	model := CostumeItemPageModel{Title: "Costume items · " + actor.Name, Production: production, Actor: actor, Form: CostumeItemFormView{Status: storage.StatusFind}, Statuses: costumeItemStatuses}
	model.Actors, model.ItemTypes = h.selectors(ctx, production.ID)
	for _, item := range items {
		model.Items = append(model.Items, h.view(ctx, production.ID, item))
	}
	if err := h.addInventoryThumbnails(ctx, &model); err != nil {
		return CostumeItemPageModel{}, err
	}
	model.Empty = len(model.Items) == 0
	return model, nil
}

func (h *CostumeItemHandler) addInventoryThumbnails(ctx context.Context, model *CostumeItemPageModel) error {
	if h.photos == nil || len(model.Items) == 0 {
		return nil
	}
	firstPhotos, err := h.photos.ListFirstReadyByActor(ctx, model.Production.ID, model.Actor.ID)
	if err != nil {
		return h.photoError("list inventory thumbnails", err)
	}
	photoByItem := make(map[int64]storage.CostumeItemPhoto, len(firstPhotos))
	for _, photo := range firstPhotos {
		photoByItem[photo.CostumeItemID] = photo
	}
	for index := range model.Items {
		item := &model.Items[index]
		if photo, ok := photoByItem[item.ID]; ok {
			item.ThumbnailURL = costumeItemDetailPath(model.Production.ID, model.Actor.ID, item.ID) + "/photos/" + strconv.FormatInt(photo.ID, 10) + "/thumbnail"
		}
	}
	return nil
}

func (h *CostumeItemHandler) scopedItem(ctx context.Context, productionID, actorID, itemID int64) (storage.Production, storage.Actor, storage.CostumeItem, error) {
	production, actor, err := h.scope(ctx, productionID, actorID)
	if err != nil {
		return storage.Production{}, storage.Actor{}, storage.CostumeItem{}, err
	}
	item, err := h.items.Get(ctx, productionID, itemID)
	if err != nil {
		return storage.Production{}, storage.Actor{}, storage.CostumeItem{}, h.storageError("get costume item", err)
	}
	if item.ProductionID != productionID || item.ActorID != actorID {
		return storage.Production{}, storage.Actor{}, storage.CostumeItem{}, costumeItemNotFound()
	}
	return production, actor, item, nil
}

func (h *CostumeItemHandler) detailModel(ctx context.Context, production storage.Production, actor storage.Actor, item storage.CostumeItem) (CostumeItemPageModel, error) {
	view := h.view(ctx, production.ID, item)
	model := CostumeItemPageModel{
		Title:      item.Code + " · " + production.Name,
		Production: production,
		Actor:      actor,
		Item:       &view,
		Statuses:   costumeItemStatuses,
	}
	if err := h.addPhotos(ctx, &model, item); err != nil {
		return CostumeItemPageModel{}, err
	}
	return model, nil
}

func (h *CostumeItemHandler) addPhotos(ctx context.Context, model *CostumeItemPageModel, item storage.CostumeItem) error {
	if h.photos == nil {
		return nil
	}
	photos, err := h.photos.List(ctx, item.ProductionID, item.ID)
	if err != nil {
		return h.photoError("list costume item photos", err)
	}
	base := costumeItemDetailPath(item.ProductionID, item.ActorID, item.ID) + "/photos/"
	model.Photos = make([]CostumeItemPhotoView, 0, len(photos))
	for _, photo := range photos {
		photoBase := base + strconv.FormatInt(photo.ID, 10) + "/"
		model.Photos = append(model.Photos, CostumeItemPhotoView{
			CostumeItemPhoto: photo,
			OriginalURL:      photoBase + "original",
			DisplayURL:       photoBase + "display",
			ThumbnailURL:     photoBase + "thumbnail",
		})
		if photo.Status == storage.PhotoStatusPending {
			model.HasPending = true
		}
	}
	return nil
}

func (h *CostumeItemHandler) view(ctx context.Context, productionID int64, item storage.CostumeItem) CostumeItemView {
	view := CostumeItemView{ID: item.ID, Production: item.ProductionID, ActorID: item.ActorID, ItemTypeID: item.ItemTypeID, Code: item.Code, Description: item.Description, Status: item.Status, Progress: item.Progress, NextAction: item.NextAction, Blocker: item.Blocker, Notes: item.Notes, Archived: item.ArchivedAt != nil, UpdatedAt: formatUpdatedAt(item.UpdatedAt)}
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

func parseCostumeItemRequest(r *http.Request) (*multipart.FileHeader, error) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil && r.Header.Get("Content-Type") != "" {
		return nil, &Error{Status: http.StatusBadRequest, Message: "Unable to read the costume item form.", Err: fmt.Errorf("parse costume item content type: %w", err)}
	}
	if mediaType != "multipart/form-data" {
		if err := r.ParseForm(); err != nil {
			return nil, &Error{Status: http.StatusBadRequest, Message: "Unable to read the costume item form.", Err: fmt.Errorf("parse costume item form: %w", err)}
		}
		return nil, nil
	}
	if err := r.ParseMultipartForm(costumeItemMultipartMemory); err != nil {
		return nil, &Error{Status: http.StatusBadRequest, Message: "Unable to read the costume item form.", Err: fmt.Errorf("parse multipart costume item form: %w", err)}
	}
	files := r.MultipartForm.File["photo"]
	if len(files) > 1 {
		return nil, &Error{Status: http.StatusBadRequest, Message: "Choose only one photo.", Err: errors.New("web: multiple costume item photos submitted")}
	}
	if len(files) == 0 {
		return nil, nil
	}
	return files[0], nil
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
	view := h.view(r.Context(), production.ID, item)
	model := CostumeItemPageModel{Title: "Edit " + item.Code, Production: production, Actor: actor, Item: &view, Form: form, Statuses: costumeItemStatuses}
	model.Actors, model.ItemTypes = h.selectors(r.Context(), production.ID)
	if err := h.addPhotos(r.Context(), &model, item); err != nil {
		return err
	}
	return h.render(w, r, status, "costume-item-page", "costume-item-edit", model)
}
func (h *CostumeItemHandler) selectors(ctx context.Context, productionID int64) ([]storage.Actor, []storage.ItemType) {
	actors, _ := h.actors.List(ctx, productionID)
	types, _ := h.itemTypes.List(ctx, productionID)
	return actors, types
}
func (h *CostumeItemHandler) renderFormError(w http.ResponseWriter, r *http.Request, status int, model CostumeItemPageModel) error {
	model.Actors, model.ItemTypes = h.selectors(r.Context(), model.Production.ID)
	if model.Form.Editing && model.Item != nil {
		item, err := h.items.Get(r.Context(), model.Production.ID, model.Item.ID)
		if err != nil {
			return h.storageError("get costume item", err)
		}
		if err := h.addPhotos(r.Context(), &model, item); err != nil {
			return err
		}
	}
	if IsHTMX(r) {
		SetTrigger(w, "costume-item:error")
		name := "costume-item-form"
		if model.Form.Editing {
			name = "costume-item-edit"
		}
		return RenderFragment(w, h.pages, name, status, model)
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
		if err := h.addInventoryThumbnails(r.Context(), &model); err != nil {
			return err
		}
		model.Empty = len(model.Items) == 0
		return h.render(w, r, status, "costume-items-page", "costume-item-form", model)
	}
	return h.render(w, r, status, "costume-item-page", "costume-item-edit", model)
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

func (h *CostumeItemHandler) photoError(operation string, err error) error {
	status, message := http.StatusInternalServerError, "Unable to load costume item photos."
	if strings.HasPrefix(operation, "upload") {
		message = "Unable to save the costume item photo."
	}
	if errors.Is(err, storage.ErrNotFound) {
		status, message = http.StatusNotFound, "That costume item photo was not found."
	}
	return &Error{Status: status, Message: message, Err: fmt.Errorf("%s: %w", operation, err)}
}

func photoServiceUnavailable() error {
	return &Error{Status: http.StatusInternalServerError, Message: "Costume item photos are unavailable.", Err: errors.New("web: photo service is required")}
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

func costumeItemPhotoID(r *http.Request) (int64, error) {
	values := []string{r.PathValue("photo")}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	for i, part := range parts {
		if part == "photos" && i+1 < len(parts) {
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
			return 0, &Error{Status: http.StatusBadRequest, Message: "Choose a valid photo.", Err: fmt.Errorf("web: invalid photo id %q", value)}
		}
		return id, nil
	}
	return 0, &Error{Status: http.StatusBadRequest, Message: "A photo is required.", Err: errors.New("web: missing photo id")}
}

func costumeItemPhotoVariant(r *http.Request) string {
	if variant := r.PathValue("variant"); variant != "" {
		return variant
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	for i, part := range parts {
		if part == "photos" && i+2 < len(parts) {
			return parts[i+2]
		}
	}
	return ""
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
