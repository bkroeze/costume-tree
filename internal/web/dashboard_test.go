package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"costume-tree/internal/storage"
)

type contextCleanup func()

func dashboardFixture(t *testing.T) (*DashboardHandler, storage.Production, storage.Actor, storage.CostumeItem, contextCleanup) {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "dashboard.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	productions := storage.NewProductionRepository(db)
	actors := storage.NewActorRepository(db)
	types := storage.NewItemTypeRepository(db)
	items := storage.NewCostumeItemRepository(db)
	ctx := context.Background()
	production, err := productions.Create(ctx, storage.CreateProductionInput{Name: "Macbeth"})
	if err != nil {
		t.Fatal(err)
	}
	actor, err := actors.Create(ctx, storage.CreateActorInput{ProductionID: production.ID, Name: "Ada"})
	if err != nil {
		t.Fatal(err)
	}
	typ, err := types.Create(ctx, storage.CreateItemTypeInput{ProductionID: production.ID, Name: "Cloak"})
	if err != nil {
		t.Fatal(err)
	}
	item, err := items.Create(ctx, storage.CreateCostumeItemInput{ProductionID: production.ID, ActorID: actor.ID, ItemTypeID: typ.ID, Status: storage.StatusMake, Progress: 25, Blocker: "Waiting for fabric"})
	if err != nil {
		t.Fatal(err)
	}
	h := NewDashboardHandler(productions, actors, types, items, storage.NewDashboardQueries(db))
	return h, production, actor, item, func() { _ = db.Close() }
}

func dashboardRequest(method, target string, values url.Values) *http.Request {
	request := httptest.NewRequest(method, target, strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return request
}

func assertWorkflowStatusOptions(t *testing.T, body string) {
	t.Helper()
	offset := 0
	for _, status := range []string{storage.StatusFind, storage.StatusMake, storage.StatusFit, storage.StatusAlterations, storage.StatusComplete} {
		token := `<option value="` + status + `"`
		index := strings.Index(body[offset:], token)
		if index < 0 {
			t.Fatalf("status option %q missing or out of order in %s", status, body)
		}
		offset += index + len(token)
	}
	choiceCount := strings.Count(body, `<option value="`) - strings.Count(body, `<option value="">`)
	if choiceCount != 5 {
		t.Fatalf("status choice count = %d in %s", choiceCount, body)
	}
	for _, oldStatus := range []string{"Not Started", "In Progress", "Blocked", "Ready"} {
		if strings.Contains(body, `<option value="`+oldStatus+`"`) {
			t.Fatalf("old status option %q present in %s", oldStatus, body)
		}
	}
}

func TestDashboardAndWorkspaceRenderActiveData(t *testing.T) {
	h, production, actor, item, cleanup := dashboardFixture(t)
	defer cleanup()
	response := httptest.NewRecorder()
	if err := h.Dashboard(response, httptest.NewRequest(http.MethodGet, "/production/"+strconv.FormatInt(production.ID, 10)+"/dashboard", nil)); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Macbeth dashboard") || !strings.Contains(response.Body.String(), "Ada") || !strings.Contains(response.Body.String(), "25%") || !strings.Contains(response.Body.String(), "Blocked by notes: 1") {
		t.Fatalf("dashboard response = %d %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	if !strings.Contains(body, `<a class="badge" href="/production/`+strconv.FormatInt(production.ID, 10)+`/dashboard">Macbeth</a>`) {
		t.Fatalf("production badge does not link to dashboard: %s", body)
	}
	if !strings.Contains(body, `href="/production/`+strconv.FormatInt(production.ID, 10)+`/dashboard">Refresh</a>`) {
		t.Fatalf("dashboard refresh link missing: %s", body)
	}
	if !strings.Contains(body, `<nav class="eyebrow" aria-label="Breadcrumb"><a href="/production/`+strconv.FormatInt(production.ID, 10)+`/dashboard">Production</a><span aria-hidden="true"> / </span><a href="/production/`+strconv.FormatInt(production.ID, 10)+`/dashboard">dashboard</a></nav>`) {
		t.Fatalf("dashboard breadcrumb links missing: %s", body)
	}
	for _, want := range []string{
		`href="/production/` + strconv.FormatInt(production.ID, 10) + `/summary"`,
		`href="/production/` + strconv.FormatInt(production.ID, 10) + `/items"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("dashboard data view link %q missing: %s", want, body)
		}
	}
	legendStart := strings.Index(body, `<div class="status-legend">`)
	if legendStart < 0 {
		t.Fatalf("actor status legend missing: %s", body)
	}
	legendEnd := strings.Index(body[legendStart:], `</div>`)
	if legendEnd < 0 {
		t.Fatalf("actor status legend is incomplete: %s", body)
	}
	legend := body[legendStart : legendStart+legendEnd]
	offset := 0
	for _, statusCount := range []string{"Fit 0", "Find 0", "Make 1", "Alterations 0", "Complete 0"} {
		index := strings.Index(legend[offset:], statusCount)
		if index < 0 {
			t.Fatalf("actor status count %q missing or out of order: %s", statusCount, legend)
		}
		offset += index + len(statusCount)
	}
	for _, label := range []string{"Total", "Complete", "Active work", "Blocked"} {
		if !strings.Contains(response.Body.String(), `<p class="kpi-title">`+label+`</p>`) {
			t.Fatalf("KPI %q missing in %s", label, response.Body.String())
		}
	}
	workspace := httptest.NewRecorder()
	if err := h.Workspace(workspace, httptest.NewRequest(http.MethodGet, "/production/"+strconv.FormatInt(production.ID, 10)+"/workspace/"+strconv.FormatInt(actor.ID, 10), nil)); err != nil {
		t.Fatal(err)
	}
	workspaceBody := workspace.Body.String()
	wantEditURL := `/production/` + strconv.FormatInt(production.ID, 10) + `/actors/` + strconv.FormatInt(actor.ID, 10) + `/items/` + strconv.FormatInt(item.ID, 10) + `/edit`
	if !strings.Contains(workspaceBody, `class="id-tag" href="`+wantEditURL+`">`+item.Code+`</a>`) {
		t.Fatalf("workspace item id does not link to edit: %s", workspaceBody)
	}
	if workspace.Code != http.StatusOK || !strings.Contains(workspaceBody, "C-0001") || !strings.Contains(workspaceBody, "Waiting for fabric") || !strings.Contains(workspaceBody, "Cloak") || !strings.Contains(workspaceBody, `name="description"`) || !strings.Contains(workspaceBody, `name="next_action"`) || strings.Contains(workspaceBody, `name="progress"`) || strings.Contains(workspaceBody, `<span class="status-brutal status-example">Make</span>`) {
		t.Fatalf("workspace response = %d %s", workspace.Code, workspaceBody)
	}
	if !strings.Contains(workspaceBody, `<nav class="eyebrow" aria-label="Breadcrumb"><a href="/production/`+strconv.FormatInt(production.ID, 10)+`/dashboard">Production</a><span aria-hidden="true"> / </span><a href="/production/`+strconv.FormatInt(production.ID, 10)+`/workspace/`+strconv.FormatInt(actor.ID, 10)+`">actor workspace</a></nav>`) {
		t.Fatalf("workspace breadcrumb links missing: %s", workspaceBody)
	}
	if !strings.Contains(workspaceBody, `href="/production/`+strconv.FormatInt(production.ID, 10)+`/workspace/`+strconv.FormatInt(actor.ID, 10)+`">Refresh</a>`) {
		t.Fatalf("workspace refresh link missing: %s", workspaceBody)
	}
	assertWorkflowStatusOptions(t, workspaceBody)
	if !strings.Contains(workspaceBody, `hx-target="#workspace-item-`+strconv.FormatInt(item.ID, 10)+`-feedback"`) || !strings.Contains(workspaceBody, `id="workspace-item-`+strconv.FormatInt(item.ID, 10)+`-feedback"`) {
		t.Fatalf("workspace save feedback target missing: %s", workspaceBody)
	}
}

func TestWorkspaceHTMXUpdateRendersInlineFeedback(t *testing.T) {
	h, production, actor, item, cleanup := dashboardFixture(t)
	defer cleanup()
	path := "/production/" + strconv.FormatInt(production.ID, 10) + "/workspace/" + strconv.FormatInt(actor.ID, 10)
	current := item.UpdatedAt.UTC().Format(time.RFC3339Nano)

	assertFeedback := func(t *testing.T, response *httptest.ResponseRecorder, status int, class, message string) {
		t.Helper()
		body := response.Body.String()
		if response.Code != status || !strings.Contains(body, `<span class="form-feedback `+class+`"`) || !strings.Contains(body, message) {
			t.Fatalf("workspace HTMX feedback = %d %s", response.Code, body)
		}
		if strings.Contains(body, "<!doctype html>") || strings.Contains(body, "workspace-content") || strings.Contains(body, "Active inventory") {
			t.Fatalf("workspace HTMX feedback rendered page content: %s", body)
		}
	}

	invalid := dashboardRequest(http.MethodPost, path, url.Values{
		"item_id":    {strconv.FormatInt(item.ID, 10)},
		"updated_at": {current},
		"status":     {storage.StatusMake},
		"progress":   {"101"},
	})
	invalid.Header.Set("HX-Request", "true")
	response := httptest.NewRecorder()
	if err := h.Workspace(response, invalid); err != nil {
		t.Fatal(err)
	}
	assertFeedback(t, response, http.StatusUnprocessableEntity, "form-feedback--error", "Progress must be a whole number from 0 to 100.")

	stale := dashboardRequest(http.MethodPost, path, url.Values{
		"item_id":    {strconv.FormatInt(item.ID, 10)},
		"updated_at": {"2000-01-01T00:00:00Z"},
		"status":     {storage.StatusAlterations},
		"progress":   {"10"},
	})
	stale.Header.Set("HX-Request", "true")
	response = httptest.NewRecorder()
	if err := h.Workspace(response, stale); err != nil {
		t.Fatal(err)
	}
	assertFeedback(t, response, http.StatusConflict, "form-feedback--error", "This item changed in another window. Reload before saving.")

	updated := dashboardRequest(http.MethodPost, path, url.Values{
		"item_id":    {strconv.FormatInt(item.ID, 10)},
		"updated_at": {current},
		"status":     {storage.StatusAlterations},
		"progress":   {"10"},
	})
	updated.Header.Set("HX-Request", "true")
	response = httptest.NewRecorder()
	if err := h.Workspace(response, updated); err != nil {
		t.Fatal(err)
	}
	assertFeedback(t, response, http.StatusOK, "form-feedback--success", "C-0001 updated.")
}

func TestWorkspaceRejectsStaleTimestampAndUpdatesStatus(t *testing.T) {
	h, production, actor, item, cleanup := dashboardFixture(t)
	defer cleanup()
	path := "/production/" + strconv.FormatInt(production.ID, 10) + "/workspace/" + strconv.FormatInt(actor.ID, 10)
	stale := dashboardRequest(http.MethodPost, path, url.Values{"item_id": {strconv.FormatInt(item.ID, 10)}, "updated_at": {"2000-01-01T00:00:00Z"}, "description": {"Changed description"}, "status": {storage.StatusAlterations}, "progress": {"10"}})
	response := httptest.NewRecorder()
	if err := h.Workspace(response, stale); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "changed in another window") {
		t.Fatalf("stale response = %d %s", response.Code, response.Body.String())
	}
	current := item.UpdatedAt.UTC().Format(time.RFC3339Nano)
	updated := dashboardRequest(http.MethodPost, path, url.Values{"item_id": {strconv.FormatInt(item.ID, 10)}, "updated_at": {current}, "description": {"Changed description"}, "status": {storage.StatusAlterations}, "progress": {"10"}})
	response = httptest.NewRecorder()
	if err := h.Workspace(response, updated); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != path {
		t.Fatalf("update response = %d location %q", response.Code, response.Header().Get("Location"))
	}
	saved, err := h.items.Get(context.Background(), production.ID, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Status != storage.StatusAlterations || saved.Progress != 75 || saved.Description != "Changed description" || saved.Blocker != "Waiting for fabric" {
		t.Fatalf("updated item = %#v", saved)
	}
}

func TestWorkspaceStatusChangeAutomaticallySetsProgress(t *testing.T) {
	h, production, actor, item, cleanup := dashboardFixture(t)
	defer cleanup()
	path := "/production/" + strconv.FormatInt(production.ID, 10) + "/workspace/" + strconv.FormatInt(actor.ID, 10)
	request := dashboardRequest(http.MethodPost, path, url.Values{
		"item_id":    {strconv.FormatInt(item.ID, 10)},
		"updated_at": {item.UpdatedAt.UTC().Format(time.RFC3339Nano)},
		"status":     {storage.StatusComplete},
		"progress":   {"99"},
	})
	response := httptest.NewRecorder()
	if err := h.Workspace(response, request); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != path {
		t.Fatalf("complete response = %d location %q", response.Code, response.Header().Get("Location"))
	}
	saved, err := h.items.Get(context.Background(), production.ID, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Status != storage.StatusComplete || saved.Progress != 100 {
		t.Fatalf("completed item = %#v", saved)
	}
}

func TestDashboardAndWorkspaceHTMXReturnFragments(t *testing.T) {
	h, production, actor, _, cleanup := dashboardFixture(t)
	defer cleanup()
	dashboard := httptest.NewRequest(http.MethodGet, "/production/"+strconv.FormatInt(production.ID, 10)+"/dashboard", nil)
	dashboard.Header.Set("HX-Request", "true")
	response := httptest.NewRecorder()
	if err := h.Dashboard(response, dashboard); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "<!doctype html>") || !strings.Contains(response.Body.String(), "Actor summaries") {
		t.Fatalf("dashboard fragment = %d %s", response.Code, response.Body.String())
	}
	workspace := httptest.NewRequest(http.MethodGet, "/production/"+strconv.FormatInt(production.ID, 10)+"/workspace/"+strconv.FormatInt(actor.ID, 10), nil)
	workspace.Header.Set("HX-Request", "true")
	response = httptest.NewRecorder()
	if err := h.Workspace(response, workspace); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "<!doctype html>") || !strings.Contains(response.Body.String(), "Active inventory") {
		t.Fatalf("workspace fragment = %d %s", response.Code, response.Body.String())
	}
}
