package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"costume-tree/internal/storage"
)

func TestItemSearchHandlerRendersCanonicalFiltersAndHTMXResults(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "search-handler.db"))
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if err := db.Migrate(ctx); err != nil { t.Fatal(err) }
	productions := storage.NewProductionRepository(db)
	actors := storage.NewActorRepository(db)
	types := storage.NewItemTypeRepository(db)
	items := storage.NewCostumeItemRepository(db)
	production, err := productions.Create(ctx, storage.CreateProductionInput{Name: "Macbeth"})
	if err != nil { t.Fatal(err) }
	actor, err := actors.Create(ctx, storage.CreateActorInput{ProductionID: production.ID, Name: "Ada"})
	if err != nil { t.Fatal(err) }
	typ, err := types.Create(ctx, storage.CreateItemTypeInput{ProductionID: production.ID, Name: "Cloak"})
	if err != nil { t.Fatal(err) }
	item, err := items.Create(ctx, storage.CreateCostumeItemInput{ProductionID: production.ID, ActorID: actor.ID, ItemTypeID: typ.ID, Description: "Blue velvet", Status: storage.StatusInProgress, Progress: 40})
	if err != nil { t.Fatal(err) }
	handler := NewItemSearchHandler(productions, storage.NewItemSearch(db))
	path := "/production/" + formatID(production.ID) + "/items?code=%20" + strings.ToLower(item.Code) + "%20&q=velvet&min=30"
	full := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.SetPathValue("production", formatID(production.ID))
	if err := handler.SearchItems(full, r); err != nil { t.Fatal(err) }
	if full.Code != http.StatusOK || !strings.Contains(full.Body.String(), "Blue velvet") || !strings.Contains(full.Body.String(), "code=C-0001") {
		t.Fatalf("full response status=%d body-len=%d body=%q", full.Code, len(full.Body.String()), full.Body.String())
	}
	hx := httptest.NewRecorder()
	hxReq := httptest.NewRequest(http.MethodGet, path, nil)
	hxReq.Header.Set("HX-Request", "true")
	hxReq.SetPathValue("production", formatID(production.ID))
	if err := handler.SearchItems(hx, hxReq); err != nil { t.Fatal(err) }
	if hx.Code != http.StatusOK || !strings.Contains(hx.Body.String(), `id="item-search-results"`) || strings.Contains(hx.Body.String(), "<!doctype html>") {
		t.Fatalf("htmx response status=%d body=%s", hx.Code, hx.Body.String())
	}
}

func formatID(id int64) string {
	return strconv.FormatInt(id, 10)
}
