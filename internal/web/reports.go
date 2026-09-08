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

//go:embed templates/reports.html
var reportTemplates embed.FS

var reportStatuses = []string{
	storage.StatusFind,
	storage.StatusMake,
	storage.StatusFit,
	storage.StatusAlterations,
	storage.StatusComplete,
}

// ReportsPageModel is the filtered report form and optional generated report.
type ReportsPageModel struct {
	Title      string
	Production storage.Production
	Status     string
	Statuses   []string
	Report     storage.FilteredReport
	HasReport  bool
	ReportURL  string
}

// ReportsHandler owns production-scoped filtered report presentation.
type ReportsHandler struct {
	productions storage.ProductionRepository
	reports     storage.ReportRepository
	pages       *template.Template
}

// NewReportsHandler constructs a filtered reports handler. A supplied
// application template set is extended with the report template.
func NewReportsHandler(productions storage.ProductionRepository, reports storage.ReportRepository, pages ...*template.Template) *ReportsHandler {
	parsed := mustReportTemplates()
	if len(pages) > 0 && pages[0] != nil {
		parsed = pages[0]
		if _, err := parsed.ParseFS(reportTemplates, "templates/reports.html"); err != nil {
			parsed = mustReportTemplates()
		}
	}
	return &ReportsHandler{productions: productions, reports: reports, pages: parsed}
}

func mustReportTemplates() *template.Template {
	parsed, err := template.ParseFS(reportTemplates, "templates/reports.html")
	if err != nil {
		panic(fmt.Sprintf("web: parse report templates: %v", err))
	}
	return parsed
}

// Reports serves GET /production/{production}/reports. A report is generated
// only when the form submits report=1, keeping the initial page lightweight.
func (h *ReportsHandler) Reports(w http.ResponseWriter, r *http.Request) error {
	if r.Method != http.MethodGet {
		return &Error{Status: http.StatusMethodNotAllowed, Message: "Method not allowed.", Err: errors.New("web: reports requires GET")}
	}
	productionID, err := costumeProductionID(r)
	if err != nil {
		return err
	}
	if h.productions == nil || h.reports == nil {
		return &Error{Status: http.StatusInternalServerError, Message: "Report storage is unavailable.", Err: errors.New("web: report repositories are required")}
	}
	production, err := h.productions.Get(r.Context(), productionID)
	if err != nil {
		return reportStorageError("get report production", err)
	}
	if production.ArchivedAt != nil {
		return &Error{Status: http.StatusConflict, Message: "That production is archived.", Err: storage.ErrArchived}
	}
	status, err := parseReportStatus(r.URL.Query())
	if err != nil {
		return err
	}
	model := ReportsPageModel{
		Title:      "Reports · " + production.Name,
		Production: production,
		Status:     status,
		Statuses:   reportStatuses,
		ReportURL:  reportURL(productionID, status),
	}
	if r.URL.Query().Get("report") == "1" {
		filterStatus := status
		if filterStatus == "all" {
			filterStatus = ""
		}
		model.Report, err = h.reports.Generate(r.Context(), storage.ReportFilter{ProductionID: productionID, Status: filterStatus})
		if err != nil {
			return reportStorageError("generate filtered report", err)
		}
		model.HasReport = true
	}
	return h.render(w, http.StatusOK, "reports-page", model)
}

// FilteredReports is a descriptive alias for composition roots.
func (h *ReportsHandler) FilteredReports(w http.ResponseWriter, r *http.Request) error {
	return h.Reports(w, r)
}

func parseReportStatus(values url.Values) (string, error) {
	status := strings.TrimSpace(values.Get("status"))
	if status == "" {
		return storage.StatusFit, nil
	}
	if strings.EqualFold(status, "all") {
		return "all", nil
	}
	for _, valid := range reportStatuses {
		if status == valid {
			return status, nil
		}
	}
	return "", badReportFilter("status", "choose all or a valid status")
}

func reportURL(productionID int64, status string) string {
	values := url.Values{}
	if status != storage.StatusFit {
		values.Set("status", status)
	}
	if encoded := values.Encode(); encoded != "" {
		return "/production/" + strconv.FormatInt(productionID, 10) + "/reports?" + encoded
	}
	return "/production/" + strconv.FormatInt(productionID, 10) + "/reports"
}

func (h *ReportsHandler) render(w http.ResponseWriter, status int, name string, model ReportsPageModel) error {
	if h.pages == nil {
		return &Error{Status: http.StatusInternalServerError, Message: "Unable to render the page.", Err: errors.New("web: report template set is required")}
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

func badReportFilter(name, reason string) error {
	return &Error{Status: http.StatusBadRequest, Message: "Invalid report filter.", Err: fmt.Errorf("web: %s: %s", name, reason)}
}

func reportStorageError(operation string, err error) error {
	status, message := http.StatusInternalServerError, "Unable to load the report."
	if errors.Is(err, storage.ErrNotFound) {
		status, message = http.StatusNotFound, "That production was not found."
	}
	if errors.Is(err, storage.ErrArchived) {
		status, message = http.StatusConflict, "That production is archived."
	}
	return &Error{Status: status, Message: message, Err: fmt.Errorf("%s: %w", operation, err)}
}
