package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"embed"
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	bulkinput "costume-tree/internal/bulk"
	"costume-tree/internal/storage"
)

//go:embed templates/bulk_import.html
var bulkImportTemplates embed.FS

const bulkSessionCookie = "costume_tree_bulk_session"

// BulkImportResult is a committed item with links back into the production
// workspace.
type BulkImportResult struct {
	Code       string
	ActorID    int64
	ActorName  string
	ItemTypeID int64
	ItemType   string
	ItemID     int64
	ActorURL   string
	ItemURL    string
}

// BulkImportPageModel is used by both the full page and HTMX fragments.
type BulkImportPageModel struct {
	Title         string
	Production    storage.Production
	Input         string
	AllowNewTypes bool
	Preview       *storage.BulkImportPreview
	Token         string
	Results       []BulkImportResult
	Error         string
	Success       string
}

// BulkImportHandler owns the bulk import page and its short-lived canonical
// preview store. The importer performs all writes and transaction checks.
type BulkImportHandler struct {
	productions storage.ProductionRepository
	importer    *storage.BulkImporter
	pages       *template.Template
	previews    *bulkPreviewStore
}

// NewBulkImportHandler constructs a bulk import handler. A supplied template
// set is extended with the bulk-import templates; omitting it uses the feature
// templates by themselves.
func NewBulkImportHandler(productions storage.ProductionRepository, importer *storage.BulkImporter, pages ...*template.Template) *BulkImportHandler {
	parsed := mustBulkImportTemplates()
	if len(pages) > 0 && pages[0] != nil {
		parsed = pages[0]
		if _, err := parsed.ParseFS(bulkImportTemplates, "templates/bulk_import.html"); err != nil {
			parsed = mustBulkImportTemplates()
		}
	}
	return &BulkImportHandler{productions: productions, importer: importer, pages: parsed, previews: newBulkPreviewStore()}
}

func mustBulkImportTemplates() *template.Template {
	parsed, err := template.ParseFS(bulkImportTemplates, "templates/bulk_import.html")
	if err != nil {
		panic(fmt.Sprintf("web: parse bulk import templates: %v", err))
	}
	return parsed
}

// BulkImport dispatches GET, preview POST, and commit POST requests for the
// /production/{production}/bulk route.
func (h *BulkImportHandler) BulkImport(w http.ResponseWriter, r *http.Request) error {
	if r.Method == http.MethodGet {
		return h.renderPage(w, r, http.StatusOK, BulkImportPageModel{})
	}
	if r.Method != http.MethodPost {
		return &Error{Status: http.StatusMethodNotAllowed, Message: "Method not allowed.", Err: errors.New("web: bulk import requires GET or POST")}
	}
	if err := r.ParseForm(); err != nil {
		return &Error{Status: http.StatusBadRequest, Message: "Unable to read the import form.", Err: err}
	}
	action := strings.ToLower(strings.TrimSpace(r.FormValue("action")))
	if action == "commit" || r.FormValue("token") != "" || r.FormValue("preview_token") != "" {
		return h.Commit(w, r)
	}
	return h.Preview(w, r)
}

// Preview parses and resolves the pasted import without writing records.
func (h *BulkImportHandler) Preview(w http.ResponseWriter, r *http.Request) error {
	if r.Method != http.MethodPost {
		return &Error{Status: http.StatusMethodNotAllowed, Message: "Method not allowed.", Err: errors.New("web: bulk preview requires POST")}
	}
	production, err := h.production(r.Context(), r)
	if err != nil {
		return err
	}
	input := bulkFormInput(r)
	allow := bulkCheckbox(r.FormValue("allow_new_types"))
	blocks, parseErr := bulkinput.Parse(input)
	model := BulkImportPageModel{Title: "Bulk import · " + production.Name, Production: production, Input: input, AllowNewTypes: allow}
	if parseErr != nil {
		model.Error = parseErr.Error()
		return h.render(w, r, http.StatusUnprocessableEntity, "bulk-import-page", "bulk-import-preview", model)
	}
	if h.importer == nil {
		return &Error{Status: http.StatusInternalServerError, Message: "Bulk import storage is unavailable.", Err: errors.New("web: bulk importer is required")}
	}
	preview, importErr := h.importer.Preview(r.Context(), production.ID, blocks, allow)
	if importErr != nil {
		return h.storageError("preview bulk import", importErr)
	}
	model.Preview = &preview
	if !preview.Valid {
		model.Error = strings.Join(preview.Errors, " ")
		return h.render(w, r, http.StatusUnprocessableEntity, "bulk-import-page", "bulk-import-preview", model)
	}
	session := bulkSession(r)
	token := h.previews.put(preview, session)
	model.Token = token
	return h.render(w, r, http.StatusOK, "bulk-import-page", "bulk-import-preview", model)
}

// Commit consumes a retained canonical preview and atomically writes it.
func (h *BulkImportHandler) Commit(w http.ResponseWriter, r *http.Request) error {
	if r.Method != http.MethodPost {
		return &Error{Status: http.StatusMethodNotAllowed, Message: "Method not allowed.", Err: errors.New("web: bulk commit requires POST")}
	}
	production, err := h.production(r.Context(), r)
	if err != nil {
		return err
	}
	token := strings.TrimSpace(r.FormValue("token"))
	if token == "" {
		token = strings.TrimSpace(r.FormValue("preview_token"))
	}
	preview, ok, lookupErr := h.previews.get(token, production.ID, bulkSession(r))
	if lookupErr != nil {
		status := http.StatusConflict
		if errors.Is(lookupErr, errBulkPreviewSession) {
			status = http.StatusForbidden
		}
		model := BulkImportPageModel{Title: "Bulk import · " + production.Name, Production: production, Error: lookupErr.Error()}
		return h.render(w, r, status, "bulk-import-page", "bulk-import-result", model)
	}
	if !ok {
		model := BulkImportPageModel{Title: "Bulk import · " + production.Name, Production: production, Error: "That preview has expired or was already committed."}
		return h.render(w, r, http.StatusConflict, "bulk-import-page", "bulk-import-result", model)
	}
	if h.importer == nil {
		return &Error{Status: http.StatusInternalServerError, Message: "Bulk import storage is unavailable.", Err: errors.New("web: bulk importer is required")}
	}
	items, commitErr := h.importer.Commit(r.Context(), preview)
	if commitErr != nil {
		status := http.StatusConflict
		if errors.Is(commitErr, storage.ErrBulkInvalidPreview) || errors.Is(commitErr, storage.ErrBulkUnknownType) {
			status = http.StatusUnprocessableEntity
		}
		model := BulkImportPageModel{Title: "Bulk import · " + production.Name, Production: production, Input: previewInput(preview), AllowNewTypes: preview.AllowNewTypes, Error: commitErr.Error()}
		return h.render(w, r, status, "bulk-import-page", "bulk-import-result", model)
	}
	h.previews.consume(token)
	model := BulkImportPageModel{Title: "Bulk import · " + production.Name, Production: production, Input: previewInput(preview), AllowNewTypes: preview.AllowNewTypes, Success: fmt.Sprintf("Imported %d costume item%s.", len(items), pluralSuffix(len(items)))}
	model.Results = make([]BulkImportResult, 0, len(items))
	for index, item := range items {
		row := storage.BulkImportRow{}
		if index < len(preview.Rows) {
			row = preview.Rows[index]
		}
		actorName, typeName := row.ActorName, row.ItemTypeName
		model.Results = append(model.Results, BulkImportResult{Code: item.Code, ActorID: item.ActorID, ActorName: actorName, ItemTypeID: item.ItemTypeID, ItemType: typeName, ItemID: item.ID, ActorURL: bulkActorURL(production.ID, item.ActorID), ItemURL: bulkItemURL(production.ID, item.ActorID, item.ID)})
	}
	return h.render(w, r, http.StatusOK, "bulk-import-page", "bulk-import-result", model)
}

// Import is a conventional alias for BulkImport.
func (h *BulkImportHandler) Import(w http.ResponseWriter, r *http.Request) error {
	return h.BulkImport(w, r)
}

func (h *BulkImportHandler) production(ctx context.Context, r *http.Request) (storage.Production, error) {
	if h.productions == nil {
		return storage.Production{}, &Error{Status: http.StatusInternalServerError, Message: "Production storage is unavailable.", Err: errors.New("web: production repository is required")}
	}
	id, err := bulkProductionID(r)
	if err != nil {
		return storage.Production{}, err
	}
	production, err := h.productions.Get(ctx, id)
	if err != nil {
		return storage.Production{}, h.storageError("get production", err)
	}
	if production.ArchivedAt != nil {
		return storage.Production{}, &Error{Status: http.StatusConflict, Message: "That production is archived.", Err: storage.ErrArchived}
	}
	return production, nil
}

func (h *BulkImportHandler) renderPage(w http.ResponseWriter, r *http.Request, status int, model BulkImportPageModel) error {
	if model.Production.ID == 0 {
		production, err := h.production(r.Context(), r)
		if err != nil {
			return err
		}
		model.Production = production
		model.Title = "Bulk import · " + production.Name
	}
	return h.render(w, r, status, "bulk-import-page", "bulk-import-preview", model)
}

func (h *BulkImportHandler) render(w http.ResponseWriter, r *http.Request, status int, fullName, fragmentName string, model BulkImportPageModel) error {
	if h.pages == nil {
		return &Error{Status: http.StatusInternalServerError, Message: "Unable to render the page.", Err: errors.New("web: bulk template set is required")}
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

func (h *BulkImportHandler) storageError(operation string, err error) error {
	if errors.Is(err, storage.ErrNotFound) {
		return &Error{Status: http.StatusNotFound, Message: "Production not found.", Err: err}
	}
	return &Error{Status: http.StatusInternalServerError, Message: "Unable to " + operation + ".", Err: err}
}

type bulkPreviewEntry struct {
	preview  storage.BulkImportPreview
	session  string
	expires  time.Time
	consumed bool
}

type bulkPreviewStore struct {
	mu      sync.Mutex
	entries map[string]bulkPreviewEntry
	used    map[string]time.Time
}

var errBulkPreviewSession = errors.New("bulk import preview belongs to another session")

func newBulkPreviewStore() *bulkPreviewStore {
	return &bulkPreviewStore{entries: make(map[string]bulkPreviewEntry), used: make(map[string]time.Time)}
}

func (s *bulkPreviewStore) put(preview storage.BulkImportPreview, session string) string {
	token := newBulkToken()
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	s.pruneLocked(now)
	s.entries[token] = bulkPreviewEntry{preview: preview, session: session, expires: now.Add(storage.BulkImportPreviewTTL)}
	return token
}

func (s *bulkPreviewStore) get(token string, productionID int64, session string) (storage.BulkImportPreview, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(time.Now().UTC())
	entry, ok := s.entries[token]
	if !ok {
		if _, used := s.used[token]; used {
			return storage.BulkImportPreview{}, false, storage.ErrBulkDuplicateSubmit
		}
		return storage.BulkImportPreview{}, false, nil
	}
	if entry.preview.ProductionID != productionID {
		return storage.BulkImportPreview{}, false, storage.ErrBulkStalePreview
	}
	if entry.session != "" && entry.session != session {
		return storage.BulkImportPreview{}, false, errBulkPreviewSession
	}
	return entry.preview, true, nil
}

func (s *bulkPreviewStore) consume(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.entries[token]; ok {
		delete(s.entries, token)
		s.used[token] = time.Now().UTC()
	}
}

func (s *bulkPreviewStore) pruneLocked(now time.Time) {
	for token, entry := range s.entries {
		if !now.Before(entry.expires) {
			delete(s.entries, token)
		}
	}
	for token, at := range s.used {
		if now.Sub(at) > storage.BulkImportPreviewTTL {
			delete(s.used, token)
		}
	}
}

func bulkSession(r *http.Request) string {
	if r == nil {
		return ""
	}
	cookie, err := r.Cookie(bulkSessionCookie)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(cookie.Value)
}

func newBulkToken() string {
	var data [32]byte
	if _, err := rand.Read(data[:]); err != nil {
		return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(time.Now().UnixNano(), 10)))
	}
	return base64.RawURLEncoding.EncodeToString(data[:])
}

func bulkFormInput(r *http.Request) string {
	for _, field := range []string{"input", "bulk", "content", "text"} {
		if value := r.FormValue(field); value != "" {
			return value
		}
	}
	return r.FormValue("input")
}

func bulkCheckbox(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return value == "1" || value == "true" || value == "on" || value == "yes"
}

func previewInput(preview storage.BulkImportPreview) string {
	var builder strings.Builder
	lastActor := ""
	for index, row := range preview.Rows {
		if row.ActorName != lastActor {
			if index > 0 {
				builder.WriteByte('\n')
			}
			builder.WriteString(row.ActorName)
			lastActor = row.ActorName
		}
		builder.WriteByte('\n')
		builder.WriteString(row.ItemTypeName)
	}
	return builder.String()
}

func pluralSuffix(count int) string {
	if count == 1 {
		return ""
	}
	return "s"
}

func bulkProductionID(r *http.Request) (int64, error) {
	value := ""
	if r != nil {
		value = r.PathValue("production")
		if value == "" {
			parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
			for index := range parts {
				if parts[index] == "production" && index+1 < len(parts) {
					value = parts[index+1]
					break
				}
			}
		}
	}
	id, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || id <= 0 {
		return 0, &Error{Status: http.StatusBadRequest, Message: "A valid production is required.", Err: fmt.Errorf("web: parse production id %q", value)}
	}
	return id, nil
}

func bulkActorURL(productionID, actorID int64) string {
	return "/production/" + strconv.FormatInt(productionID, 10) + "/actors/" + strconv.FormatInt(actorID, 10) + "/items"
}

func bulkItemURL(productionID, actorID, itemID int64) string {
	return bulkActorURL(productionID, actorID) + "/" + strconv.FormatInt(itemID, 10)
}
