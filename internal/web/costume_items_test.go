package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"costume-tree/internal/storage"
)

func costumeItemTestRepos(t *testing.T) (storage.ProductionRepository, storage.ActorRepository, storage.ItemTypeRepository, storage.CostumeItemRepository, context.Context) {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "costume-items.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return storage.NewProductionRepository(db), storage.NewActorRepository(db), storage.NewItemTypeRepository(db), storage.NewCostumeItemRepository(db), ctx
}

func costumeItemRequest(method, path string, values url.Values) *http.Request {
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

func costumeItemFixture(t *testing.T) (*CostumeItemHandler, storage.Production, storage.Actor, storage.ItemType, context.Context) {
	t.Helper()
	productions, actors, types, items, ctx := costumeItemTestRepos(t)
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
	return NewCostumeItemHandler(productions, actors, types, items), production, actor, typ, ctx
}

func TestCostumeItemCreateAllocatesUniqueCodesAndAllowsDuplicates(t *testing.T) {
	h, production, actor, typ, _ := costumeItemFixture(t)
	path := "/production/" + strconv.FormatInt(production.ID, 10) + "/actors/" + strconv.FormatInt(actor.ID, 10) + "/items"
	values := url.Values{"item_type_id": {strconv.FormatInt(typ.ID, 10)}, "description": {"Blue cloak"}, "status": {storage.StatusNotStarted}, "progress": {"0"}}
	first := httptest.NewRecorder()
	if err := h.CreateCostumeItem(first, costumeItemRequest(http.MethodPost, path, values)); err != nil {
		t.Fatal(err)
	}
	second := httptest.NewRecorder()
	if err := h.CreateCostumeItem(second, costumeItemRequest(http.MethodPost, path, values)); err != nil {
		t.Fatal(err)
	}
	if first.Code != http.StatusSeeOther || second.Code != http.StatusSeeOther {
		t.Fatalf("create statuses = %d/%d", first.Code, second.Code)
	}
	list := httptest.NewRecorder()
	if err := h.ListCostumeItems(list, costumeItemRequest(http.MethodGet, path, nil)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(list.Body.String(), "C-0001") || !strings.Contains(list.Body.String(), "C-0002") {
		t.Fatalf("codes missing from list: %s", list.Body.String())
	}
	if strings.Count(list.Body.String(), "Blue cloak") != 2 {
		t.Fatalf("duplicate descriptions not preserved: %s", list.Body.String())
	}
}

func TestCostumeItemRejectsCrossProductionReferencesAndCompleteProgress(t *testing.T) {
	h, production, actor, typ, ctx := costumeItemFixture(t)
	other, err := h.productions.Create(ctx, storage.CreateProductionInput{Name: "Hamlet"})
	if err != nil {
		t.Fatal(err)
	}
	otherActor, err := h.actors.Create(ctx, storage.CreateActorInput{ProductionID: other.ID, Name: "Bea"})
	if err != nil {
		t.Fatal(err)
	}
	otherType, err := h.itemTypes.Create(ctx, storage.CreateItemTypeInput{ProductionID: other.ID, Name: "Hat"})
	if err != nil {
		t.Fatal(err)
	}
	path := "/production/" + strconv.FormatInt(production.ID, 10) + "/actors/" + strconv.FormatInt(actor.ID, 10) + "/items"
	badComplete := url.Values{"item_type_id": {strconv.FormatInt(typ.ID, 10)}, "status": {storage.StatusComplete}, "progress": {"99"}}
	response := httptest.NewRecorder()
	if err := h.CreateCostumeItem(response, costumeItemRequest(http.MethodPost, path, badComplete)); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "100") {
		t.Fatalf("complete validation = %d/%s", response.Code, response.Body.String())
	}
	cross := url.Values{"item_type_id": {strconv.FormatInt(otherType.ID, 10)}, "actor_id": {strconv.FormatInt(otherActor.ID, 10)}, "progress": {"0"}}
	response = httptest.NewRecorder()
	if err := h.CreateCostumeItem(response, costumeItemRequest(http.MethodPost, path, cross)); err == nil {
		t.Fatal("cross-production type unexpectedly accepted")
	}
}

func TestCostumeItemEditRejectsStaleTimestampAndArchiveHidesFromList(t *testing.T) {
	h, production, actor, typ, ctx := costumeItemFixture(t)
	item, err := h.items.Create(ctx, storage.CreateCostumeItemInput{ProductionID: production.ID, ActorID: actor.ID, ItemTypeID: typ.ID, Description: "Original"})
	if err != nil {
		t.Fatal(err)
	}
	path := costumeItemDetailPath(production.ID, actor.ID, item.ID) + "/edit"
	stale := url.Values{"item_type_id": {strconv.FormatInt(typ.ID, 10)}, "description": {"Changed"}, "status": {storage.StatusReady}, "progress": {"20"}, "updated_at": {"2000-01-01T00:00:00Z"}}
	response := httptest.NewRecorder()
	if err := h.EditCostumeItem(response, costumeItemRequest(http.MethodPost, path, stale)); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusConflict {
		t.Fatalf("stale edit status = %d", response.Code)
	}
	archive := httptest.NewRecorder()
	if err := h.ArchiveCostumeItem(archive, costumeItemRequest(http.MethodPost, costumeItemDetailPath(production.ID, actor.ID, item.ID)+"/archive", nil)); err != nil {
		t.Fatal(err)
	}
	if archive.Code != http.StatusSeeOther {
		t.Fatalf("archive status = %d", archive.Code)
	}
	list := httptest.NewRecorder()
	if err := h.ListCostumeItems(list, costumeItemRequest(http.MethodGet, costumeItemListPath(production.ID, actor.ID), nil)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(list.Body.String(), item.Code) {
		t.Fatalf("archived code remains in active list: %s", list.Body.String())
	}
	detail := httptest.NewRecorder()
	if err := h.DetailCostumeItem(detail, costumeItemRequest(http.MethodGet, costumeItemDetailPath(production.ID, actor.ID, item.ID), nil)); err != nil {
		t.Fatal(err)
	}
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), item.Code) || !strings.Contains(detail.Body.String(), "Archived") {
		t.Fatalf("archived detail = %d/%s", detail.Code, detail.Body.String())
	}
}

func TestCostumeItemLookupIsExactAndSupportsHTMX(t *testing.T) {
	h, production, actor, typ, ctx := costumeItemFixture(t)
	item, err := h.items.Create(ctx, storage.CreateCostumeItemInput{ProductionID: production.ID, ActorID: actor.ID, ItemTypeID: typ.ID, Description: "Lookup me"})
	if err != nil {
		t.Fatal(err)
	}
	path := "/production/" + strconv.FormatInt(production.ID, 10) + "/items/" + item.Code
	full := httptest.NewRecorder()
	if err := h.LookupCostumeItem(full, costumeItemRequest(http.MethodGet, path, nil)); err != nil {
		t.Fatal(err)
	}
	if full.Code != http.StatusOK || !strings.Contains(full.Body.String(), "<html") || !strings.Contains(full.Body.String(), item.Code) {
		t.Fatalf("full lookup = %d/%s", full.Code, full.Body.String())
	}
	fragmentRequest := costumeItemRequest(http.MethodGet, path, nil)
	fragmentRequest.Header.Set("HX-Request", "true")
	fragment := httptest.NewRecorder()
	if err := h.LookupCostumeItem(fragment, fragmentRequest); err != nil {
		t.Fatal(err)
	}
	if fragment.Code != http.StatusOK || strings.Contains(fragment.Body.String(), "<html") || !strings.Contains(fragment.Body.String(), item.Code) {
		t.Fatalf("fragment lookup = %d/%s", fragment.Code, fragment.Body.String())
	}
	missing := httptest.NewRecorder()
	if err := h.LookupCostumeItem(missing, costumeItemRequest(http.MethodGet, "/production/"+strconv.FormatInt(production.ID, 10)+"/items/C-0001x", nil)); err == nil {
		t.Fatal("inexact code unexpectedly found")
	}
}
