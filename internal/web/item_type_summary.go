package web

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strconv"

	"costume-tree/internal/storage"
)

//go:embed templates/item_type_summary.html
var itemTypeSummaryTemplates embed.FS

// ItemTypeSummaryCountView is one linked status count in the summary table.
type ItemTypeSummaryCountView struct {
	Label string
	Count int
	URL   string
}

// ItemTypeSummaryView is the presentation form of one item type aggregate.
type ItemTypeSummaryView struct {
	ID         int64
	Name       string
	Total      int
	Complete   int
	InProgress int
	Blocked    int
	Ready      int
	NotStarted int
	Counts     []ItemTypeSummaryCountView
}

// ItemTypeSummaryPageModel is the full page and HTMX fragment model.
type ItemTypeSummaryPageModel struct {
	Title      string
	Production storage.Production
	Rows       []ItemTypeSummaryView
	Summaries  []ItemTypeSummaryView
}

// ItemTypeSummaryHandler owns production-scoped status summary reads.
type ItemTypeSummaryHandler struct {
	productions storage.ProductionRepository
	summaries   storage.ItemTypeSummaryRepository
	pages       *template.Template
}

// NewItemTypeSummaryHandler constructs a summary handler. A supplied template
// set is extended with the summary templates; omitting it uses embedded ones.
func NewItemTypeSummaryHandler(productions storage.ProductionRepository, summaries storage.ItemTypeSummaryRepository, pages ...*template.Template) *ItemTypeSummaryHandler {
	parsed := mustItemTypeSummaryTemplates()
	if len(pages) > 0 && pages[0] != nil {
		parsed = pages[0]
		if _, err := parsed.ParseFS(itemTypeSummaryTemplates, "templates/item_type_summary.html"); err != nil {
			parsed = mustItemTypeSummaryTemplates()
		}
	}
	return &ItemTypeSummaryHandler{productions: productions, summaries: summaries, pages: parsed}
}

func mustItemTypeSummaryTemplates() *template.Template {
	parsed, err := template.ParseFS(itemTypeSummaryTemplates, "templates/item_type_summary.html")
	if err != nil {
		panic(fmt.Sprintf("web: parse item type summary templates: %v", err))
	}
	return parsed
}

// Summary renders the production summary as a document or HTMX fragment.
func (h *ItemTypeSummaryHandler) Summary(w http.ResponseWriter, r *http.Request) error {
	if r.Method != http.MethodGet {
		return itemTypeSummaryMethodError("item type summary requires GET")
	}
	productionID, err := costumeProductionID(r)
	if err != nil {
		return err
	}
	production, err := h.production(r.Context(), productionID)
	if err != nil {
		return err
	}
	if h.summaries == nil {
		return &Error{Status: http.StatusInternalServerError, Message: "Summary storage is unavailable.", Err: errors.New("web: item type summary repository is required")}
	}
	rows, err := h.summaries.List(r.Context(), productionID)
	if err != nil {
		return h.storageError("list item type summary", err)
	}
	model := ItemTypeSummaryPageModel{Title: "Production summary · " + production.Name, Production: production}
	model.Rows = make([]ItemTypeSummaryView, 0, len(rows))
	for _, row := range rows {
		view := h.view(productionID, row)
		model.Rows = append(model.Rows, view)
	}
	model.Summaries = model.Rows
	if IsHTMX(r) {
		return RenderFragment(w, h.pages, "item-type-summary", http.StatusOK, model)
	}
	return h.renderPage(w, http.StatusOK, "item-type-summary-page", model)
}

// ItemTypeSummary is an explicit alias useful to composition roots that use
// feature names as handler methods.
func (h *ItemTypeSummaryHandler) ItemTypeSummary(w http.ResponseWriter, r *http.Request) error {
	return h.Summary(w, r)
}

// ListItemTypeSummary is an alias retained for list-oriented route wiring.
func (h *ItemTypeSummaryHandler) ListItemTypeSummary(w http.ResponseWriter, r *http.Request) error {
	return h.Summary(w, r)
}

func (h *ItemTypeSummaryHandler) view(productionID int64, row storage.ItemTypeSummary) ItemTypeSummaryView {
	return ItemTypeSummaryView{
		ID: row.ItemTypeID, Name: row.ItemTypeName,
		Total: row.Total, Complete: row.Complete, InProgress: row.InProgress,
		Blocked: row.Blocked, Ready: row.Ready, NotStarted: row.NotStarted,
		Counts: []ItemTypeSummaryCountView{
			{Label: "Total", Count: row.Total, URL: itemTypeSummaryItemsPath(productionID, row.ItemTypeID, "")},
			{Label: storage.StatusComplete, Count: row.Complete, URL: itemTypeSummaryItemsPath(productionID, row.ItemTypeID, storage.StatusComplete)},
			{Label: storage.StatusInProgress, Count: row.InProgress, URL: itemTypeSummaryItemsPath(productionID, row.ItemTypeID, storage.StatusInProgress)},
			{Label: storage.StatusBlocked, Count: row.Blocked, URL: itemTypeSummaryItemsPath(productionID, row.ItemTypeID, storage.StatusBlocked)},
			{Label: storage.StatusReady, Count: row.Ready, URL: itemTypeSummaryItemsPath(productionID, row.ItemTypeID, storage.StatusReady)},
			{Label: storage.StatusNotStarted, Count: row.NotStarted, URL: itemTypeSummaryItemsPath(productionID, row.ItemTypeID, storage.StatusNotStarted)},
		},
	}
}

func itemTypeSummaryItemsPath(productionID, itemTypeID int64, status string) string {
	query := url.Values{}
	query.Set("item_type_id", strconv.FormatInt(itemTypeID, 10))
	if status != "" {
		query.Set("status", status)
	}
	return "/production/" + strconv.FormatInt(productionID, 10) + "/items?" + query.Encode()
}

func (h *ItemTypeSummaryHandler) production(ctx context.Context, productionID int64) (storage.Production, error) {
	if h.productions == nil {
		return storage.Production{}, &Error{Status: http.StatusInternalServerError, Message: "Production storage is unavailable.", Err: errors.New("web: production repository is required")}
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

func (h *ItemTypeSummaryHandler) renderPage(w http.ResponseWriter, status int, name string, model ItemTypeSummaryPageModel) error {
	if h.pages == nil {
		return &Error{Status: http.StatusInternalServerError, Message: "Unable to render the summary.", Err: errors.New("web: template set is required")}
	}
	return RenderFragment(w, h.pages, name, status, model)
}

func (h *ItemTypeSummaryHandler) storageError(operation string, err error) error {
	status := http.StatusInternalServerError
	message := "Unable to load the production summary."
	if errors.Is(err, storage.ErrNotFound) {
		status, message = http.StatusNotFound, "The requested production was not found."
	} else if errors.Is(err, storage.ErrArchived) {
		status, message = http.StatusConflict, "That production is archived."
	}
	return &Error{Status: status, Message: message, Err: fmt.Errorf("%s: %w", operation, err)}
}

func itemTypeSummaryMethodError(message string) error {
	return &Error{Status: http.StatusMethodNotAllowed, Message: "Method not allowed.", Err: errors.New("web: " + message)}
}
