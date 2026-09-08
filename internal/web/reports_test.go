package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"costume-tree/internal/storage"
)

func TestReportsHandlerDefaultsToFitAndRendersRichTextReport(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "reports-web.db"))
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
	actor, err := actors.Create(ctx, storage.CreateActorInput{ProductionID: production.ID, Name: "Ada"})
	if err != nil {
		t.Fatal(err)
	}
	accessories, err := types.Create(ctx, storage.CreateItemTypeInput{ProductionID: production.ID, Name: "Accessories"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := items.Create(ctx, storage.CreateCostumeItemInput{
		ProductionID: production.ID, ActorID: actor.ID, ItemTypeID: accessories.ID,
		Description: "Red gloves", Status: storage.StatusFit, Progress: 50,
	}); err != nil {
		t.Fatal(err)
	}
	handler := NewReportsHandler(productions, storage.NewReportRepository(db))
	basePath := "/production/" + formatID(production.ID) + "/reports"

	initial := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, basePath, nil)
	request.SetPathValue("production", formatID(production.ID))
	if err := handler.Reports(initial, request); err != nil {
		t.Fatal(err)
	}
	initialBody := initial.Body.String()
	if initial.Code != http.StatusOK || !strings.Contains(initialBody, `option value="Fit" selected`) || strings.Contains(initialBody, `id="report-modal"`) {
		t.Fatalf("initial report page = %d/%s", initial.Code, initialBody)
	}

	generated := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, basePath+"?status=all&report=1", nil)
	request.SetPathValue("production", formatID(production.ID))
	if err := handler.Reports(generated, request); err != nil {
		t.Fatal(err)
	}
	body := generated.Body.String()
	for _, want := range []string{
		`<dialog class="report-modal" open`,
		`Macbeth costume report`,
		`Accessories</th>`,
		`Red gloves`,
		`Ada`,
		`Copy to clipboard`,
		`'text/html': new Blob`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("generated report missing %q: %s", want, body)
		}
	}
}
