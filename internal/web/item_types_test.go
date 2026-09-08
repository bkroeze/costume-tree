package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"costume-tree/internal/storage"
)

func itemTypeTestRepos(t *testing.T) (storage.ProductionRepository, storage.ItemTypeRepository, context.Context) {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "item-types.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return storage.NewProductionRepository(db), storage.NewItemTypeRepository(db), ctx
}

func itemTypeRequest(method, path string, values url.Values) *http.Request {
	var body io.Reader
	if values != nil {
		body = strings.NewReader(values.Encode())
	}
	r := httptest.NewRequest(method, path, body)
	if values != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	return r
}

func TestItemTypeSeedDefaultsIsProductionScopedAndIdempotent(t *testing.T) {
	productions, itemTypes, ctx := itemTypeTestRepos(t)
	first, err := productions.Create(ctx, storage.CreateProductionInput{Name: "Macbeth"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := productions.Create(ctx, storage.CreateProductionInput{Name: "Hamlet"})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewItemTypeHandler(productions, itemTypes)
	if err := handler.SeedDefaults(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	if err := handler.ProductionBootstrap(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	got, err := itemTypes.List(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(DefaultItemTypeNames) {
		t.Fatalf("seeded %d types, want %d", len(got), len(DefaultItemTypeNames))
	}
	other, err := itemTypes.List(ctx, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(other) != 0 {
		t.Fatalf("second production unexpectedly has item types: %#v", other)
	}
}

func TestItemTypeCreateNormalizesAndRejectsCaseInsensitiveDuplicate(t *testing.T) {
	productions, itemTypes, ctx := itemTypeTestRepos(t)
	production, err := productions.Create(ctx, storage.CreateProductionInput{Name: "Macbeth"})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewItemTypeHandler(productions, itemTypes)
	path := "/production/" + strconvFormat(production.ID) + "/item-types"
	response := httptest.NewRecorder()
	if err := handler.CreateItemType(response, itemTypeRequest(http.MethodPost, path, url.Values{"name": {"  Cloak  "}})); err != nil {
		t.Fatal(err)
	}
	_, err = itemTypes.List(ctx, production.ID)
	if err != nil {
		t.Fatal(err)
	}
	duplicate := httptest.NewRecorder()
	if err := handler.CreateItemType(duplicate, itemTypeRequest(http.MethodPost, path, url.Values{"name": {" cloak "}})); err != nil {
		t.Fatal(err)
	}
	if duplicate.Code != http.StatusConflict {
		t.Fatalf("duplicate status = %d, want %d", duplicate.Code, http.StatusConflict)
	}
	unchanged, err := itemTypes.List(ctx, production.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(unchanged) != 1 || unchanged[0].Name != "Cloak" {
		t.Fatalf("duplicate changed records: %#v", unchanged)
	}
}

func TestItemTypeListSupportsFullPageAndHTMXFragment(t *testing.T) {
	productions, itemTypes, ctx := itemTypeTestRepos(t)
	production, err := productions.Create(ctx, storage.CreateProductionInput{Name: "Macbeth"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := itemTypes.Create(ctx, storage.CreateItemTypeInput{ProductionID: production.ID, Name: "Cloak"}); err != nil {
		t.Fatal(err)
	}
	handler := NewItemTypeHandler(productions, itemTypes)
	path := "/production/" + strconvFormat(production.ID) + "/item-types"

	full := httptest.NewRecorder()
	if err := handler.ListItemTypes(full, itemTypeRequest(http.MethodGet, path, nil)); err != nil {
		t.Fatal(err)
	}
	if full.Code != http.StatusOK || !strings.Contains(full.Body.String(), "<html") || !strings.Contains(full.Body.String(), "Cloak") {
		t.Fatalf("full response status/body = %d/%s", full.Code, full.Body.String())
	}
	if !strings.Contains(full.Body.String(), `<nav class="eyebrow" aria-label="Breadcrumb"><a href="/production/`+strconvFormat(production.ID)+`/dashboard">Production</a><span aria-hidden="true"> / </span><a href="/production/`+strconvFormat(production.ID)+`/item-types">settings</a></nav>`) {
		t.Fatalf("item types breadcrumb links missing: %s", full.Body.String())
	}

	fragmentRequest := itemTypeRequest(http.MethodGet, path, nil)
	fragmentRequest.Header.Set("HX-Request", "true")
	fragment := httptest.NewRecorder()
	if err := handler.ListItemTypes(fragment, fragmentRequest); err != nil {
		t.Fatal(err)
	}
	if fragment.Code != http.StatusOK || strings.Contains(fragment.Body.String(), "<html") || !strings.Contains(fragment.Body.String(), "Cloak") {
		t.Fatalf("fragment response status/body = %d/%s", fragment.Code, fragment.Body.String())
	}
}

func TestItemTypeHandlerRequiresProductionScope(t *testing.T) {
	productions, itemTypes, _ := itemTypeTestRepos(t)
	handler := NewItemTypeHandler(productions, itemTypes)
	response := httptest.NewRecorder()
	err := handler.ListItemTypes(response, itemTypeRequest(http.MethodGet, "/item-types", nil))
	if err == nil {
		t.Fatal("unscoped list unexpectedly succeeded")
	}
	if httpErr, ok := err.(*Error); !ok || httpErr.Status != http.StatusBadRequest {
		t.Fatalf("unscoped error = %#v", err)
	}
}

func TestArchiveItemTypePreservesRecordInProductionScope(t *testing.T) {
	productions, itemTypes, ctx := itemTypeTestRepos(t)
	production, err := productions.Create(ctx, storage.CreateProductionInput{Name: "Macbeth"})
	if err != nil {
		t.Fatal(err)
	}
	item, err := itemTypes.Create(ctx, storage.CreateItemTypeInput{ProductionID: production.ID, Name: "Cloak"})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewItemTypeHandler(productions, itemTypes)
	path := "/production/" + strconvFormat(production.ID) + "/item-types/" + strconvFormat(item.ID) + "/archive"
	response := httptest.NewRecorder()
	if err := handler.ArchiveItemType(response, itemTypeRequest(http.MethodPost, path, nil)); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusSeeOther {
		t.Fatalf("archive status = %d, want %d", response.Code, http.StatusSeeOther)
	}
	archived, err := itemTypes.List(ctx, production.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(archived) != 1 || archived[0].ID != item.ID || archived[0].ArchivedAt == nil {
		t.Fatalf("archived item type = %#v", archived)
	}
}
