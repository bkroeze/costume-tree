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
	item, err := items.Create(ctx, storage.CreateCostumeItemInput{ProductionID: production.ID, ActorID: actor.ID, ItemTypeID: typ.ID, Status: storage.StatusInProgress, Progress: 25})
	if err != nil {
		t.Fatal(err)
	}
	h := NewDashboardHandler(productions, actors, items, storage.NewDashboardQueries(db))
	return h, production, actor, item, func() { _ = db.Close() }
}

func dashboardRequest(method, target string, values url.Values) *http.Request {
	request := httptest.NewRequest(method, target, strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return request
}

func TestDashboardAndWorkspaceRenderActiveData(t *testing.T) {
	h, production, actor, _, cleanup := dashboardFixture(t)
	defer cleanup()
	response := httptest.NewRecorder()
	if err := h.Dashboard(response, httptest.NewRequest(http.MethodGet, "/production/"+strconv.FormatInt(production.ID, 10)+"/dashboard", nil)); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Macbeth dashboard") || !strings.Contains(response.Body.String(), "Ada") || !strings.Contains(response.Body.String(), "25%") {
		t.Fatalf("dashboard response = %d %s", response.Code, response.Body.String())
	}
	workspace := httptest.NewRecorder()
	if err := h.Workspace(workspace, httptest.NewRequest(http.MethodGet, "/production/"+strconv.FormatInt(production.ID, 10)+"/workspace/"+strconv.FormatInt(actor.ID, 10), nil)); err != nil {
		t.Fatal(err)
	}
	if workspace.Code != http.StatusOK || !strings.Contains(workspace.Body.String(), "C-0001") || !strings.Contains(workspace.Body.String(), "25") {
		t.Fatalf("workspace response = %d %s", workspace.Code, workspace.Body.String())
	}
}

func TestWorkspaceRejectsStaleTimestampAndUpdatesStatus(t *testing.T) {
	h, production, actor, item, cleanup := dashboardFixture(t)
	defer cleanup()
	path := "/production/" + strconv.FormatInt(production.ID, 10) + "/workspace/" + strconv.FormatInt(actor.ID, 10)
	stale := dashboardRequest(http.MethodPost, path, url.Values{"item_id": {strconv.FormatInt(item.ID, 10)}, "updated_at": {"2000-01-01T00:00:00Z"}, "status": {storage.StatusBlocked}, "progress": {"10"}})
	response := httptest.NewRecorder()
	if err := h.Workspace(response, stale); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "changed in another window") {
		t.Fatalf("stale response = %d %s", response.Code, response.Body.String())
	}
	current := item.UpdatedAt.UTC().Format(time.RFC3339Nano)
	updated := dashboardRequest(http.MethodPost, path, url.Values{"item_id": {strconv.FormatInt(item.ID, 10)}, "updated_at": {current}, "status": {storage.StatusBlocked}, "progress": {"10"}})
	response = httptest.NewRecorder()
	if err := h.Workspace(response, updated); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != path {
		t.Fatalf("update response = %d location %q", response.Code, response.Header().Get("Location"))
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
