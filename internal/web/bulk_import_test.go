package web

import (
	"context"
	"costume-tree/internal/storage"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func bulkWebFixture(t *testing.T) (*BulkImportHandler, *storage.DB, storage.Production, context.Context) {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "bulk-web.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	production, err := storage.NewProductionRepository(db).Create(ctx, storage.CreateProductionInput{Name: "Macbeth"})
	if err != nil {
		t.Fatal(err)
	}
	pages, err := template.ParseFS(content, "templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	handler := NewBulkImportHandler(storage.NewProductionRepository(db), storage.NewBulkImporter(db), pages)
	return handler, db, production, ctx
}

func bulkWebRequest(method, path string, values url.Values) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(values.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return r
}

func bulkPath(productionID int64) string {
	return "/production/" + strconvFormat(productionID) + "/bulk"
}

func TestBulkImportPreviewCommitAndRepeatedRows(t *testing.T) {
	h, db, production, _ := bulkWebFixture(t)
	previewResponse := httptest.NewRecorder()
	request := bulkWebRequest(http.MethodPost, bulkPath(production.ID), url.Values{"action": {"preview"}, "input": {"Ada\nCloak\nCloak"}, "allow_new_types": {"true"}})
	request.SetPathValue("production", strconvFormat(production.ID))
	if err := h.BulkImport(previewResponse, request); err != nil {
		t.Fatal(err)
	}
	if previewResponse.Code != http.StatusOK || !strings.Contains(previewResponse.Body.String(), "Commit import") || !strings.Contains(previewResponse.Body.String(), "name=\"token\"") {
		t.Fatalf("preview = %d %s", previewResponse.Code, previewResponse.Body.String())
	}
	if !strings.Contains(previewResponse.Body.String(), `<nav class="eyebrow" aria-label="Breadcrumb"><a href="/production/`+strconvFormat(production.ID)+`/dashboard">Production</a><span aria-hidden="true"> / </span><a href="/production/`+strconvFormat(production.ID)+`/bulk">intake</a></nav>`) {
		t.Fatalf("bulk import breadcrumb links missing: %s", previewResponse.Body.String())
	}
	token := h.previewsTokenForTest()
	commitResponse := httptest.NewRecorder()
	commit := bulkWebRequest(http.MethodPost, bulkPath(production.ID), url.Values{"action": {"commit"}, "token": {token}})
	commit.SetPathValue("production", strconvFormat(production.ID))
	if err := h.BulkImport(commitResponse, commit); err != nil {
		t.Fatal(err)
	}
	if commitResponse.Code != http.StatusOK || !strings.Contains(commitResponse.Body.String(), "Imported 2 costume items") || !strings.Contains(commitResponse.Body.String(), "/actors/") {
		t.Fatalf("commit = %d %s", commitResponse.Code, commitResponse.Body.String())
	}
	items, err := storage.NewCostumeItemRepository(db).List(context.Background(), storage.CostumeItemFilter{ProductionID: production.ID})
	if err != nil || len(items) != 2 {
		t.Fatalf("items = %#v, err = %v", items, err)
	}
}

func TestBulkImportCrossProductionAndStaleTokensRejected(t *testing.T) {
	h, db, production, ctx := bulkWebFixture(t)
	other, err := storage.NewProductionRepository(db).Create(ctx, storage.CreateProductionInput{Name: "Hamlet"})
	if err != nil {
		t.Fatal(err)
	}
	request := bulkWebRequest(http.MethodPost, bulkPath(production.ID), url.Values{"input": {"Ada\nCloak"}, "allow_new_types": {"true"}})
	request.SetPathValue("production", strconvFormat(production.ID))
	if err := h.Preview(httptest.NewRecorder(), request); err != nil {
		t.Fatal(err)
	}
	token := h.previewsTokenForTest()
	cross := bulkWebRequest(http.MethodPost, bulkPath(other.ID), url.Values{"action": {"commit"}, "token": {token}})
	cross.SetPathValue("production", strconvFormat(other.ID))
	crossResponse := httptest.NewRecorder()
	if err := h.Commit(crossResponse, cross); err != nil {
		t.Fatal(err)
	}
	if crossResponse.Code != http.StatusConflict {
		t.Fatalf("cross-production status = %d", crossResponse.Code)
	}
	entry := h.previews.entries[token]
	entry.expires = time.Now().Add(-time.Minute)
	h.previews.entries[token] = entry
	stale := bulkWebRequest(http.MethodPost, bulkPath(production.ID), url.Values{"action": {"commit"}, "token": {token}})
	stale.SetPathValue("production", strconvFormat(production.ID))
	staleResponse := httptest.NewRecorder()
	if err := h.Commit(staleResponse, stale); err != nil {
		t.Fatal(err)
	}
	if staleResponse.Code != http.StatusConflict {
		t.Fatalf("stale status = %d", staleResponse.Code)
	}
}

// previewsTokenForTest returns the sole token while keeping production tests
// independent of the random token representation.
func (h *BulkImportHandler) previewsTokenForTest() string {
	h.previews.mu.Lock()
	defer h.previews.mu.Unlock()
	for token := range h.previews.entries {
		return token
	}
	return ""
}
