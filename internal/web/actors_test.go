package web

import (
	"context"
	"costume-tree/internal/storage"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func actorHandlerFixture(t *testing.T) (*ActorHandler, *storage.DB) {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "costume-tree.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	pages, err := template.ParseFS(content, "templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	return NewActorHandler(storage.NewProductionRepository(db), storage.NewActorRepository(db), pages), db
}

func actorFormRequest(method, target string, values url.Values) *http.Request {
	r := httptest.NewRequest(method, target, strings.NewReader(values.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return r
}

func TestProductionBootstrapAndActorLifecycle(t *testing.T) {
	handler, _ := actorHandlerFixture(t)

	first := httptest.NewRecorder()
	if err := handler.ProductionBootstrap(first, httptest.NewRequest(http.MethodGet, ProductionPath, nil)); err != nil {
		t.Fatal(err)
	}
	if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), "Name your production") {
		t.Fatalf("first-run response = %d %s", first.Code, first.Body.String())
	}

	created := httptest.NewRecorder()
	if err := handler.CreateProduction(created, actorFormRequest(http.MethodPost, ProductionPath, url.Values{"name": {"  Hamlet  "}})); err != nil {
		t.Fatal(err)
	}
	if created.Code != http.StatusSeeOther || created.Header().Get("Location") != "/production/1/actors" {
		t.Fatalf("create production response = %d location %q", created.Code, created.Header().Get("Location"))
	}

	invalid := httptest.NewRecorder()
	if err := handler.CreateActor(invalid, actorFormRequest(http.MethodPost, ActorsPath, url.Values{"name": {"  "}, "role": {"Ghost"}})); err != nil {
		t.Fatal(err)
	}
	if invalid.Code != http.StatusUnprocessableEntity || !strings.Contains(invalid.Body.String(), "Enter an actor name") || !strings.Contains(invalid.Body.String(), "Ghost") {
		t.Fatalf("validation response = %d %s", invalid.Code, invalid.Body.String())
	}

	createdActor := httptest.NewRecorder()
	if err := handler.CreateActor(createdActor, actorFormRequest(http.MethodPost, ActorsPath, url.Values{"name": {"Banquo"}, "role": {"Thane"}, "notes": {"Fitting pending"}})); err != nil {
		t.Fatal(err)
	}
	if createdActor.Code != http.StatusSeeOther {
		t.Fatalf("create actor response = %d", createdActor.Code)
	}

	list := httptest.NewRecorder()
	if err := handler.ListActors(list, httptest.NewRequest(http.MethodGet, ActorsPath, nil)); err != nil {
		t.Fatal(err)
	}
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), "Banquo") || !strings.Contains(list.Body.String(), "Thane") {
		t.Fatalf("actor list = %d %s", list.Code, list.Body.String())
	}
	if !strings.Contains(list.Body.String(), `href="/production/1/actors/1">Edit</a>`) {
		t.Fatalf("actor edit link missing or incorrect: %s", list.Body.String())
	}
}

func TestActorHTMXValidationAndArchive(t *testing.T) {
	handler, db := actorHandlerFixture(t)
	ctx := context.Background()
	production, err := storage.NewProductionRepository(db).Create(ctx, storage.CreateProductionInput{Name: "Macbeth"})
	if err != nil {
		t.Fatal(err)
	}
	actor, err := storage.NewActorRepository(db).Create(ctx, storage.CreateActorInput{ProductionID: production.ID, Name: "Macduff"})
	if err != nil {
		t.Fatal(err)
	}

	invalidRequest := actorFormRequest(http.MethodPost, ActorsPath, url.Values{"name": {""}})
	invalidRequest.Header.Set("HX-Request", "true")
	invalid := httptest.NewRecorder()
	if err := handler.CreateActor(invalid, invalidRequest); err != nil {
		t.Fatal(err)
	}
	if invalid.Code != http.StatusUnprocessableEntity || strings.Contains(invalid.Body.String(), "<html") || !strings.Contains(invalid.Body.String(), "Enter an actor name") {
		t.Fatalf("HTMX validation = %d %s", invalid.Code, invalid.Body.String())
	}

	archiveRequest := httptest.NewRequest(http.MethodPost, ActorsPath+"/"+strconvFormat(actor.ID)+"/archive", nil)
	archiveRequest.SetPathValue("id", strconvFormat(actor.ID))
	archiveRequest.Header.Set("HX-Request", "true")
	archived := httptest.NewRecorder()
	if err := handler.ArchiveActor(archived, archiveRequest); err != nil {
		t.Fatal(err)
	}
	if archived.Code != http.StatusOK || archived.Header().Get("HX-Trigger") != "actor:archived" || !strings.Contains(archived.Body.String(), "Archived actors") {
		t.Fatalf("HTMX archive = %d trigger=%q body=%s", archived.Code, archived.Header().Get("HX-Trigger"), archived.Body.String())
	}
	active, err := storage.NewActorRepository(db).List(ctx, production.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 0 {
		t.Fatalf("active actors after archive = %#v", active)
	}
}

func strconvFormat(value int64) string {
	return strconv.FormatInt(value, 10)
}
