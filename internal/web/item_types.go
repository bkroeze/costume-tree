package web

import (
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

//go:embed templates/item_types.html
var itemTypeTemplates embed.FS

// DefaultItemTypeNames are seeded for each newly-created production.
var DefaultItemTypeNames = []string{
	"Pants", "Skirt", "Shirt", "Dress", "Jacket", "Shoes", "Hat", "Accessory", "Other",
}

type ItemTypeView struct {
	ID         int64
	Name       string
	Archived   bool
	CanRestore bool
}

type ItemTypeListView struct {
	Title      string
	Production storage.Production
	ItemTypes  []ItemTypeView
	Active     []ItemTypeView
	Archived   []ItemTypeView
	Error      string
	Success    string
}

// ItemTypeHandler owns production-scoped item-type HTTP behavior.
type ItemTypeHandler struct {
	productions storage.ProductionRepository
	itemTypes   storage.ItemTypeRepository
	pages       *template.Template
}

// NewItemTypeHandler constructs a handler. A supplied template is extended
// with the item-type templates; omitting it uses the embedded feature templates.
func NewItemTypeHandler(productions storage.ProductionRepository, itemTypes storage.ItemTypeRepository, pages ...*template.Template) *ItemTypeHandler {
	parsed := mustItemTypeTemplates()
	if len(pages) > 0 && pages[0] != nil {
		parsed = pages[0]
		if _, err := parsed.ParseFS(itemTypeTemplates, "templates/item_types.html"); err != nil {
			parsed = mustItemTypeTemplates()
		}
	}
	return &ItemTypeHandler{productions: productions, itemTypes: itemTypes, pages: parsed}
}

func mustItemTypeTemplates() *template.Template {
	parsed, err := template.ParseFS(itemTypeTemplates, "templates/item_types.html")
	if err != nil {
		panic(fmt.Sprintf("web: parse item type templates: %v", err))
	}
	return parsed
}

// SeedDefaults creates the standard vocabulary after a production is created.
// Existing records, including archived records, are never changed.
func (h *ItemTypeHandler) SeedDefaults(ctx context.Context, productionID int64) error {
	if err := h.ensureProduction(ctx, productionID); err != nil {
		return err
	}
	for _, name := range DefaultItemTypeNames {
		if err := h.createIfMissing(ctx, productionID, name); err != nil {
			return err
		}
	}
	return nil
}

// ProductionBootstrap is a composition-root-friendly alias for SeedDefaults.
func (h *ItemTypeHandler) ProductionBootstrap(ctx context.Context, productionID int64) error {
	return h.SeedDefaults(ctx, productionID)
}

// ListItemTypes renders all active and archived types for the URL production.
func (h *ItemTypeHandler) ListItemTypes(w http.ResponseWriter, r *http.Request) error {
	productionID, err := itemTypeRequestID(r, "production", "production_id")
	if err != nil {
		return err
	}
	if r.Method != http.MethodGet {
		return itemTypeMethodError("item type list requires GET")
	}
	production, err := h.production(r.Context(), productionID)
	if err != nil {
		return err
	}
	items, err := h.itemTypes.List(r.Context(), productionID, true)
	if err != nil {
		return h.storageError("list item types", err)
	}
	model := ItemTypeListView{Title: "Item types · " + production.Name, Production: production}
	for _, item := range items {
		view := ItemTypeView{ID: item.ID, Name: item.Name, Archived: item.ArchivedAt != nil, CanRestore: item.ArchivedAt != nil}
		model.ItemTypes = append(model.ItemTypes, view)
		if view.Archived {
			model.Archived = append(model.Archived, view)
		} else {
			model.Active = append(model.Active, view)
		}
	}
	if IsHTMX(r) {
		return RenderFragment(w, h.pages, "item-types-list", http.StatusOK, model)
	}
	return RenderFragment(w, h.pages, "item-types-page", http.StatusOK, model)
}

// CreateItemType creates one trimmed, case-insensitively unique type.
func (h *ItemTypeHandler) CreateItemType(w http.ResponseWriter, r *http.Request) error {
	productionID, err := itemTypeRequestID(r, "production", "production_id")
	if err != nil {
		return err
	}
	if r.Method != http.MethodPost {
		return itemTypeMethodError("item type create requires POST")
	}
	if err := r.ParseForm(); err != nil {
		return &Error{Status: http.StatusBadRequest, Message: "Unable to read the item type form.", Err: fmt.Errorf("parse item type form: %w", err)}
	}
	name := normalizeItemTypeName(r.FormValue("name"))
	if name == "" {
		return h.formError(w, r, productionID, "Enter an item type name.")
	}
	if err := h.ensureProduction(r.Context(), productionID); err != nil {
		return err
	}
	if h.hasDuplicate(r.Context(), productionID, name, 0) {
		return h.formError(w, r, productionID, "That item type already exists.", http.StatusConflict)
	}
	if _, err := h.itemTypes.Create(r.Context(), storage.CreateItemTypeInput{ProductionID: productionID, Name: name}); err != nil {
		if h.hasDuplicate(r.Context(), productionID, name, 0) {
			return h.formError(w, r, productionID, "That item type already exists.", http.StatusConflict)
		}
		return h.storageError("create item type", err)
	}
	SetTrigger(w, "item-type:created")
	Redirect(w, r, itemTypesPath(productionID), http.StatusSeeOther)
	return nil
}

// RenameItemType changes a name while retaining its production and ID.
func (h *ItemTypeHandler) RenameItemType(w http.ResponseWriter, r *http.Request) error {
	productionID, itemTypeID, err := requestItemTypeIDs(r)
	if err != nil {
		return err
	}
	if r.Method != http.MethodPost && r.Method != http.MethodPatch {
		return itemTypeMethodError("item type rename requires POST or PATCH")
	}
	if err := r.ParseForm(); err != nil {
		return &Error{Status: http.StatusBadRequest, Message: "Unable to read the item type form.", Err: fmt.Errorf("parse item type form: %w", err)}
	}
	name := normalizeItemTypeName(r.FormValue("name"))
	if name == "" {
		return h.formError(w, r, productionID, "Enter an item type name.")
	}
	if err := h.ensureProduction(r.Context(), productionID); err != nil {
		return err
	}
	if _, err := h.itemTypes.Get(r.Context(), productionID, itemTypeID); err != nil {
		return h.storageError("get item type", err)
	}
	if h.hasDuplicate(r.Context(), productionID, name, itemTypeID) {
		return h.formError(w, r, productionID, "That item type already exists.", http.StatusConflict)
	}
	if _, err := h.itemTypes.Update(r.Context(), storage.UpdateItemTypeInput{ProductionID: productionID, ID: itemTypeID, Name: name}); err != nil {
		if h.hasDuplicate(r.Context(), productionID, name, itemTypeID) {
			return h.formError(w, r, productionID, "That item type already exists.", http.StatusConflict)
		}
		return h.storageError("rename item type", err)
	}
	SetTrigger(w, "item-type:renamed")
	Redirect(w, r, itemTypesPath(productionID), http.StatusSeeOther)
	return nil
}

// ArchiveItemType soft-archives a type, preserving references from costume items.
func (h *ItemTypeHandler) ArchiveItemType(w http.ResponseWriter, r *http.Request) error {
	productionID, itemTypeID, err := requestItemTypeIDs(r)
	if err != nil {
		return err
	}
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		return itemTypeMethodError("item type archive requires POST or DELETE")
	}
	if err := h.ensureProduction(r.Context(), productionID); err != nil {
		return err
	}
	if _, err := h.itemTypes.Get(r.Context(), productionID, itemTypeID); err != nil {
		return h.storageError("get item type", err)
	}
	if err := h.itemTypes.Archive(r.Context(), productionID, itemTypeID); err != nil {
		return h.storageError("archive item type", err)
	}
	SetTrigger(w, "item-type:archived")
	Redirect(w, r, itemTypesPath(productionID), http.StatusSeeOther)
	return nil
}

// RestoreItemType uses the optional restore extension while retaining the
// Wave 2 ItemTypeRepository contract. A repository without the extension
// returns a clear error rather than pretending persistence succeeded.
func (h *ItemTypeHandler) RestoreItemType(w http.ResponseWriter, r *http.Request) error {
	productionID, itemTypeID, err := requestItemTypeIDs(r)
	if err != nil {
		return err
	}
	if r.Method != http.MethodPost && r.Method != http.MethodPatch {
		return itemTypeMethodError("item type restore requires POST or PATCH")
	}
	if err := h.ensureProduction(r.Context(), productionID); err != nil {
		return err
	}
	item, err := h.itemTypes.Get(r.Context(), productionID, itemTypeID)
	if err != nil {
		return h.storageError("get item type", err)
	}
	if item.ArchivedAt == nil {
		return &Error{Status: http.StatusConflict, Message: "That item type is already active.", Err: errors.New("web: item type is not archived")}
	}
	restorer, ok := h.itemTypes.(interface {
		Restore(context.Context, int64, int64) error
	})
	if !ok {
		return &Error{Status: http.StatusNotImplemented, Message: "Item type restore is unavailable.", Err: errors.New("web: item type repository does not support restore")}
	}
	if err := restorer.Restore(r.Context(), productionID, itemTypeID); err != nil {
		return h.storageError("restore item type", err)
	}
	SetTrigger(w, "item-type:restored")
	Redirect(w, r, itemTypesPath(productionID), http.StatusSeeOther)
	return nil
}

// Production and ItemTypes are aliases useful when registering a conventional
// production settings page.
func (h *ItemTypeHandler) Production(w http.ResponseWriter, r *http.Request) error {
	return h.ListItemTypes(w, r)
}
func (h *ItemTypeHandler) ItemTypes(w http.ResponseWriter, r *http.Request) error {
	return h.ListItemTypes(w, r)
}

func (h *ItemTypeHandler) createIfMissing(ctx context.Context, productionID int64, name string) error {
	if h.hasDuplicate(ctx, productionID, name, 0) {
		return nil
	}
	if _, err := h.itemTypes.Create(ctx, storage.CreateItemTypeInput{ProductionID: productionID, Name: name}); err != nil && !h.hasDuplicate(ctx, productionID, name, 0) {
		return h.storageError("seed item type", err)
	}
	return nil
}

func (h *ItemTypeHandler) ensureProduction(ctx context.Context, productionID int64) error {
	_, err := h.production(ctx, productionID)
	return err
}

func (h *ItemTypeHandler) production(ctx context.Context, productionID int64) (storage.Production, error) {
	if h.productions == nil {
		return storage.Production{}, &Error{Status: http.StatusInternalServerError, Message: "Production storage is unavailable.", Err: errors.New("web: production repository is nil")}
	}
	production, err := h.productions.Get(ctx, productionID)
	if err != nil {
		return storage.Production{}, h.storageError("get production", err)
	}
	if production.ArchivedAt != nil {
		return storage.Production{}, &Error{Status: http.StatusConflict, Message: "That production is archived.", Err: storage.ErrArchived}
	}
	return production, nil
}

func (h *ItemTypeHandler) hasDuplicate(ctx context.Context, productionID int64, name string, excludeID int64) bool {
	if h.itemTypes == nil {
		return false
	}
	items, err := h.itemTypes.List(ctx, productionID, true)
	if err != nil {
		return false
	}
	for _, item := range items {
		if item.ID != excludeID && strings.EqualFold(normalizeItemTypeName(item.Name), name) {
			return true
		}
	}
	return false
}

func (h *ItemTypeHandler) formError(w http.ResponseWriter, r *http.Request, productionID int64, message string, statuses ...int) error {
	status := http.StatusUnprocessableEntity
	if len(statuses) > 0 {
		status = statuses[0]
	}
	if IsHTMX(r) {
		SetTrigger(w, "item-type:error")
		return RenderFragment(w, h.pages, "item-type-feedback", status, struct{ Error string }{Error: message})
	}
	production, err := h.production(r.Context(), productionID)
	if err != nil {
		return err
	}
	return RenderFragment(w, h.pages, "item-types-page", status, ItemTypeListView{Title: "Item types · " + production.Name, Production: production, Error: message})
}

func (h *ItemTypeHandler) storageError(operation string, err error) error {
	status := http.StatusInternalServerError
	message := "Unable to update item types."
	if errors.Is(err, storage.ErrNotFound) {
		status, message = http.StatusNotFound, "The requested item type or production was not found."
	} else if errors.Is(err, storage.ErrArchived) {
		status, message = http.StatusConflict, "That record is archived."
	}
	return &Error{Status: status, Message: message, Err: fmt.Errorf("%s: %w", operation, err)}
}

func normalizeItemTypeName(name string) string { return strings.TrimSpace(name) }
func itemTypesPath(productionID int64) string {
	return "/production/" + strconv.FormatInt(productionID, 10) + "/item-types"
}

func itemTypeMethodError(message string) error {
	return &Error{Status: http.StatusMethodNotAllowed, Message: "Method not allowed.", Err: errors.New("web: " + message)}
}

func requestItemTypeIDs(r *http.Request) (int64, int64, error) {
	productionID, err := itemTypeRequestID(r, "production", "production_id")
	if err != nil {
		return 0, 0, err
	}
	itemTypeID, err := itemTypeRequestID(r, "itemType", "item_type_id")
	if err != nil {
		return 0, 0, err
	}
	return productionID, itemTypeID, nil
}

func itemTypeRequestID(r *http.Request, pathName, queryName string) (int64, error) {
	values := []string{r.PathValue(pathName), r.URL.Query().Get(queryName)}
	if pathName == "itemType" {
		values = append(values, r.URL.Query().Get("itemTypeID"), r.URL.Query().Get("item_id"))
	}
	for _, value := range values {
		if value == "" {
			continue
		}
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil || id <= 0 {
			return 0, &Error{Status: http.StatusBadRequest, Message: "Invalid production or item type.", Err: fmt.Errorf("web: invalid id %q", value)}
		}
		return id, nil
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	for i, part := range parts {
		if (pathName == "production" && part == "production" && i+1 < len(parts)) || (pathName == "itemType" && (part == "item-types" || part == "item_type") && i+1 < len(parts)) {
			id, err := strconv.ParseInt(parts[i+1], 10, 64)
			if err == nil && id > 0 {
				return id, nil
			}
		}
	}
	return 0, &Error{Status: http.StatusBadRequest, Message: "A production and item type are required.", Err: errors.New("web: missing scoped id")}
}
