package web

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"costume-tree/internal/storage"
)

//go:embed templates/item_search.html
var itemSearchTemplates embed.FS

// ItemSearchFilters is the canonical URL-facing form of an item search.
type ItemSearchFilters struct {
	Code        string
	Text        string
	ActorID     int64
	ItemTypeID  int64
	Status      string
	Blocked     bool
	Incomplete  bool
	MinProgress *int
	MaxProgress *int
	Page        int
	Limit       int
}

// ItemSearchPageModel is shared by the full page and HTMX results fragment.
type ItemSearchPageModel struct {
	Title        string
	Production   storage.Production
	Filters      ItemSearchFilters
	Results      []storage.ItemSearchResult
	Count        int
	Offset       int
	HasPrevious  bool
	HasNext      bool
	PreviousURL  string
	NextURL      string
	CanonicalURL string
	ClearURL     string
	Empty        bool
}

// ItemSearchHandler owns production-scoped item search presentation.
type ItemSearchHandler struct {
	productions storage.ProductionRepository
	search      storage.ItemSearchRepository
	pages       *template.Template
}

// NewItemSearchHandler constructs an item search handler. An optional parsed
// application template set is extended with the feature template.
func NewItemSearchHandler(productions storage.ProductionRepository, search storage.ItemSearchRepository, pages ...*template.Template) *ItemSearchHandler {
	parsed := mustItemSearchTemplates()
	if len(pages) > 0 && pages[0] != nil {
		parsed = pages[0]
		if _, err := parsed.ParseFS(itemSearchTemplates, "templates/item_search.html"); err != nil {
			parsed = mustItemSearchTemplates()
		}
	}
	return &ItemSearchHandler{productions: productions, search: search, pages: parsed}
}

func mustItemSearchTemplates() *template.Template {
	parsed, err := template.ParseFS(itemSearchTemplates, "templates/item_search.html")
	if err != nil {
		panic(fmt.Sprintf("web: parse item search templates: %v", err))
	}
	return parsed
}

// SearchItems serves GET /production/{production}/items and its HTMX results
// fragment. Every request is scoped to the production in the URL.
func (h *ItemSearchHandler) SearchItems(w http.ResponseWriter, r *http.Request) error {
	if r.Method != http.MethodGet {
		return &Error{Status: http.StatusMethodNotAllowed, Message: "Method not allowed.", Err: errors.New("web: item search requires GET")}
	}
	productionID, err := costumeProductionID(r)
	if err != nil {
		return err
	}
	if h.productions == nil || h.search == nil {
		return &Error{Status: http.StatusInternalServerError, Message: "Item search storage is unavailable.", Err: errors.New("web: item search repositories are required")}
	}
	production, err := h.productions.Get(r.Context(), productionID)
	if err != nil {
		return itemSearchStorageError("get search production", err)
	}
	if production.ArchivedAt != nil {
		return &Error{Status: http.StatusConflict, Message: "That production is archived.", Err: storage.ErrArchived}
	}
	filters, err := parseItemSearchFilters(r.URL.Query())
	if err != nil {
		return err
	}
	query := storage.ItemSearchFilter{
		ProductionID: productionID,
		Code:         filters.Code, Text: filters.Text, ActorID: filters.ActorID,
		ItemTypeID: filters.ItemTypeID, Status: filters.Status,
		Blocked: filters.Blocked, Incomplete: filters.Incomplete,
		MinProgress: filters.MinProgress, MaxProgress: filters.MaxProgress,
		Limit: filters.Limit, Offset: (filters.Page - 1) * filters.Limit,
	}
	results, err := h.search.Search(r.Context(), query)
	if err != nil {
		return itemSearchStorageError("search costume items", err)
	}
	model := ItemSearchPageModel{
		Title: "Search items · " + production.Name, Production: production,
		Filters: filters, Results: results, Count: len(results), Offset: query.Offset,
		HasPrevious: filters.Page > 1, HasNext: len(results) == filters.Limit,
		CanonicalURL: itemSearchURL(productionID, filters),
		ClearURL:     itemSearchURL(productionID, ItemSearchFilters{Page: 1, Limit: filters.Limit}),
		Empty:        len(results) == 0,
	}
	if model.HasPrevious {
		previous := filters
		previous.Page--
		model.PreviousURL = itemSearchURL(productionID, previous)
	}
	if model.HasNext {
		next := filters
		next.Page++
		model.NextURL = itemSearchURL(productionID, next)
	}
	if IsHTMX(r) {
		return RenderFragment(w, h.pages, "item-search-results", http.StatusOK, model)
	}
	return h.render(w, http.StatusOK, "item-search-page", model)
}

// Items is a conventional short alias for mux method values.
func (h *ItemSearchHandler) Items(w http.ResponseWriter, r *http.Request) error {
	return h.SearchItems(w, r)
}

func (h *ItemSearchHandler) render(w http.ResponseWriter, status int, name string, model ItemSearchPageModel) error {
	if h.pages == nil {
		return &Error{Status: http.StatusInternalServerError, Message: "Unable to render the page.", Err: errors.New("web: item search template set is required")}
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

func parseItemSearchFilters(values url.Values) (ItemSearchFilters, error) {
	filters := ItemSearchFilters{Page: 1, Limit: storage.DefaultItemSearchLimit}
	filters.Code = strings.ToUpper(strings.TrimSpace(values.Get("code")))
	filters.Text = strings.TrimSpace(firstQuery(values, "q", "text", "search"))
	var err error
	if filters.ActorID, err = positiveQueryInt(values, "actor", "actor_id"); err != nil {
		return ItemSearchFilters{}, err
	}
	if filters.ItemTypeID, err = positiveQueryInt(values, "type", "item_type", "item_type_id"); err != nil {
		return ItemSearchFilters{}, err
	}
	filters.Status = strings.TrimSpace(values.Get("status"))
	if filters.Status != "" && !validSearchStatus(filters.Status) {
		return ItemSearchFilters{}, badSearchFilter("status", "choose a valid status")
	}
	if filters.Blocked, err = boolQuery(values.Get("blocked")); err != nil {
		return ItemSearchFilters{}, badSearchFilter("blocked", "use true or false")
	}
	if filters.Incomplete, err = boolQuery(values.Get("incomplete")); err != nil {
		return ItemSearchFilters{}, badSearchFilter("incomplete", "use true or false")
	}
	if filters.MinProgress, err = optionalProgress(values, "min", "min_progress"); err != nil {
		return ItemSearchFilters{}, err
	}
	if filters.MaxProgress, err = optionalProgress(values, "max", "max_progress"); err != nil {
		return ItemSearchFilters{}, err
	}
	if filters.MinProgress != nil && filters.MaxProgress != nil && *filters.MinProgress > *filters.MaxProgress {
		return ItemSearchFilters{}, badSearchFilter("progress", "minimum cannot exceed maximum")
	}
	if page := strings.TrimSpace(values.Get("page")); page != "" {
		filters.Page, err = strconv.Atoi(page)
		if err != nil || filters.Page < 1 || filters.Page > 2_000_000_000 {
			return ItemSearchFilters{}, badSearchFilter("page", "use a positive page number")
		}
	}
	if limit := strings.TrimSpace(values.Get("limit")); limit != "" {
		filters.Limit, err = strconv.Atoi(limit)
		if err != nil || filters.Limit < 1 {
			return ItemSearchFilters{}, badSearchFilter("limit", "use a positive result limit")
		}
		if filters.Limit > storage.MaxItemSearchLimit {
			filters.Limit = storage.MaxItemSearchLimit
		}
	}
	return filters, nil
}

func itemSearchURL(productionID int64, filters ItemSearchFilters) string {
	path := "/production/" + strconv.FormatInt(productionID, 10) + "/items"
	values := url.Values{}
	if filters.Code != "" {
		values.Set("code", strings.TrimSpace(filters.Code))
	}
	if filters.Text != "" {
		values.Set("q", strings.TrimSpace(filters.Text))
	}
	if filters.ActorID > 0 {
		values.Set("actor", strconv.FormatInt(filters.ActorID, 10))
	}
	if filters.ItemTypeID > 0 {
		values.Set("type", strconv.FormatInt(filters.ItemTypeID, 10))
	}
	if filters.Status != "" {
		values.Set("status", filters.Status)
	}
	if filters.Blocked {
		values.Set("blocked", "true")
	}
	if filters.Incomplete {
		values.Set("incomplete", "true")
	}
	if filters.MinProgress != nil {
		values.Set("min", strconv.Itoa(*filters.MinProgress))
	}
	if filters.MaxProgress != nil {
		values.Set("max", strconv.Itoa(*filters.MaxProgress))
	}
	if filters.Limit > 0 && filters.Limit != storage.DefaultItemSearchLimit {
		values.Set("limit", strconv.Itoa(filters.Limit))
	}
	if filters.Page > 1 {
		values.Set("page", strconv.Itoa(filters.Page))
	}
	if encoded := values.Encode(); encoded != "" {
		return path + "?" + encoded
	}
	return path
}

func firstQuery(values url.Values, keys ...string) string {
	for _, key := range keys {
		if value := values.Get(key); value != "" {
			return value
		}
	}
	return ""
}

func positiveQueryInt(values url.Values, keys ...string) (int64, error) {
	value := firstQuery(values, keys...)
	if value == "" {
		return 0, nil
	}
	id, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || id <= 0 {
		return 0, badSearchFilter(keys[0], "use a positive integer")
	}
	return id, nil
}

func optionalProgress(values url.Values, keys ...string) (*int, error) {
	value := firstQuery(values, keys...)
	if value == "" {
		return nil, nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || n < 0 || n > 100 {
		return nil, badSearchFilter(keys[0], "use a number from 0 to 100")
	}
	return &n, nil
}

func boolQuery(value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "0", "false", "off", "no":
		return false, nil
	case "1", "true", "on", "yes":
		return true, nil
	default:
		return false, errors.New("invalid boolean")
	}
}

func validSearchStatus(status string) bool {
	switch status {
	case storage.StatusNotStarted, storage.StatusInProgress, storage.StatusBlocked, storage.StatusReady, storage.StatusComplete:
		return true
	}
	return false
}

func badSearchFilter(name, reason string) error {
	return &Error{Status: http.StatusBadRequest, Message: "Invalid search filter.", Err: fmt.Errorf("web: %s: %s", name, reason)}
}

func itemSearchStorageError(operation string, err error) error {
	status, message := http.StatusInternalServerError, "Unable to search costume items."
	if errors.Is(err, storage.ErrNotFound) {
		status, message = http.StatusNotFound, "That production was not found."
	}
	if errors.Is(err, storage.ErrArchived) {
		status, message = http.StatusConflict, "That production is archived."
	}
	return &Error{Status: status, Message: message, Err: fmt.Errorf("%s: %w", operation, err)}
}
