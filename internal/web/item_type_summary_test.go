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

func TestItemTypeSummaryRendersCanonicalFilterLinksAndHTMXMutation(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "summary-web.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	productions := storage.NewProductionRepository(db)
	actors := storage.NewActorRepository(db)
	types := storage.NewItemTypeRepository(db)
	items := storage.NewCostumeItemRepository(db)
	production, err := productions.Create(ctx, storage.CreateProductionInput{Name: "Macbeth"})
	if err != nil {
		t.Fatal(err)
	}
	actor, err := actors.Create(ctx, storage.CreateActorInput{ProductionID: production.ID, Name: "A"})
	if err != nil {
		t.Fatal(err)
	}
	cloak, err := types.Create(ctx, storage.CreateItemTypeInput{ProductionID: production.ID, Name: "Cloak"})
	if err != nil {
		t.Fatal(err)
	}
	item, err := items.Create(ctx, storage.CreateCostumeItemInput{ProductionID: production.ID, ActorID: actor.ID, ItemTypeID: cloak.ID, Status: storage.StatusFit, Progress: 50, Blocker: "  Waiting for shoes  "})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewItemTypeSummaryHandler(productions, storage.NewItemTypeSummaryRepository(db))
	path := "/production/" + strconv.FormatInt(production.ID, 10) + "/summary"
	full := httptest.NewRecorder()
	if err := handler.Summary(full, httptest.NewRequest(http.MethodGet, path, nil)); err != nil {
		t.Fatal(err)
	}
	body := full.Body.String()
	if full.Code != http.StatusOK || !strings.Contains(body, "Production summary") || !strings.Contains(body, "Cloak") {
		t.Fatalf("full summary = %d/%s", full.Code, body)
	}
	if !strings.Contains(body, `href="/production/`+strconv.FormatInt(production.ID, 10)+`/items?item_type_id=`+strconv.FormatInt(cloak.ID, 10)+`&amp;status=Fit"`) {
		t.Fatalf("missing canonical fit link: %s", body)
	}
	if !strings.Contains(body, `href="/production/`+strconv.FormatInt(production.ID, 10)+`/items?blocked=true&amp;item_type_id=`+strconv.FormatInt(cloak.ID, 10)+`"`) {
		t.Fatalf("missing canonical blocked link: %s", body)
	}
	if !strings.Contains(body, `href="/production/`+strconv.FormatInt(production.ID, 10)+`/items?item_type_id=`+strconv.FormatInt(cloak.ID, 10)+`"`) {
		t.Fatalf("missing canonical total link: %s", body)
	}
	if !strings.Contains(body, `<th scope="col">Total</th><th scope="col">Find</th><th scope="col">Make</th><th scope="col">Fit</th><th scope="col">Alterations</th><th scope="col">Complete</th><th scope="col">Blocked</th>`) {
		t.Fatalf("summary headers are out of workflow order: %s", body)
	}
	if strings.Contains(body, `status=Complete"`) {
		t.Fatalf("zero complete count should not be linked: %s", body)
	}

	item.Status = storage.StatusComplete
	item.Progress = 100
	if _, err := items.Update(ctx, storage.UpdateCostumeItemInput{ProductionID: production.ID, ID: item.ID, ActorID: actor.ID, ItemTypeID: cloak.ID, Status: item.Status, Progress: item.Progress, Blocker: item.Blocker}); err != nil {
		t.Fatal(err)
	}
	fragmentRequest := httptest.NewRequest(http.MethodGet, path, nil)
	fragmentRequest.Header.Set("HX-Request", "true")
	fragment := httptest.NewRecorder()
	if err := handler.Summary(fragment, fragmentRequest); err != nil {
		t.Fatal(err)
	}
	fragmentBody := fragment.Body.String()
	if fragment.Code != http.StatusOK || strings.Contains(fragmentBody, "<!doctype html") || !strings.Contains(fragmentBody, ">Complete</th>") || !strings.Contains(fragmentBody, `status=Complete`) || !strings.Contains(fragmentBody, `blocked=true`) {
		t.Fatalf("updated HTMX summary = %d/%s", fragment.Code, fragmentBody)
	}
}
