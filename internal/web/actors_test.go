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
	if !strings.Contains(first.Body.String(), `<nav class="eyebrow" aria-label="Breadcrumb"><a href="/production">Workspace</a><span aria-hidden="true"> / </span><a href="/production">first run</a></nav>`) {
		t.Fatalf("first-run breadcrumb links missing: %s", first.Body.String())
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
	if !strings.Contains(list.Body.String(), `<nav class="eyebrow" aria-label="Breadcrumb"><a href="/production/1/dashboard">Workspace</a><span aria-hidden="true"> / </span><a href="/production/1/actors">cast</a></nav>`) {
		t.Fatalf("actor breadcrumb links missing: %s", list.Body.String())
	}
}

func TestActorAddEditAddResetsSharedForm(t *testing.T) {
	handler, db := actorHandlerFixture(t)
	ctx := context.Background()
	production, err := storage.NewProductionRepository(db).Create(ctx, storage.CreateProductionInput{Name: "Hamlet"})
	if err != nil {
		t.Fatal(err)
	}

	create := actorFormRequest(http.MethodPost, ActorsPath, url.Values{"name": {"Banquo"}, "role": {"Thane"}})
	create.Header.Set("HX-Request", "true")
	created := httptest.NewRecorder()
	if err := handler.CreateActor(created, create); err != nil {
		t.Fatal(err)
	}
	if created.Code != http.StatusNoContent || created.Header().Get("HX-Redirect") != "/production/1/actors" || created.Header().Get("HX-Trigger") != "actor:created" {
		t.Fatalf("HTMX create = %d redirect=%q trigger=%q", created.Code, created.Header().Get("HX-Redirect"), created.Header().Get("HX-Trigger"))
	}

	assertActorFormIsReset(t, handler, production.ID)
	actors, err := storage.NewActorRepository(db).List(ctx, production.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(actors) != 1 {
		t.Fatalf("actors after create = %#v", actors)
	}

	edit := actorFormRequest(http.MethodPost, ActorsPath+"/"+strconvFormat(actors[0].ID), url.Values{"name": {"Banquo updated"}, "role": {"Lead"}})
	edit.SetPathValue("id", strconvFormat(actors[0].ID))
	edit.Header.Set("HX-Request", "true")
	updated := httptest.NewRecorder()
	if err := handler.EditActor(updated, edit); err != nil {
		t.Fatal(err)
	}
	if updated.Code != http.StatusNoContent || updated.Header().Get("HX-Redirect") != "/production/1/actors" || updated.Header().Get("HX-Trigger") != "actor:updated" {
		t.Fatalf("HTMX edit = %d redirect=%q trigger=%q", updated.Code, updated.Header().Get("HX-Redirect"), updated.Header().Get("HX-Trigger"))
	}

	assertActorFormIsReset(t, handler, production.ID)

	addSecond := actorFormRequest(http.MethodPost, ActorsPath, url.Values{"name": {"Ophelia"}})
	addSecond.Header.Set("HX-Request", "true")
	added := httptest.NewRecorder()
	if err := handler.CreateActor(added, addSecond); err != nil {
		t.Fatal(err)
	}
	if added.Code != http.StatusNoContent || added.Header().Get("HX-Redirect") != "/production/1/actors" {
		t.Fatalf("HTMX second create = %d redirect=%q", added.Code, added.Header().Get("HX-Redirect"))
	}

	list := httptest.NewRecorder()
	if err := handler.ListActors(list, httptest.NewRequest(http.MethodGet, ActorsPath, nil)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(list.Body.String(), "Banquo updated") || !strings.Contains(list.Body.String(), "Ophelia") {
		t.Fatalf("actor list after add-edit-add = %s", list.Body.String())
	}
	assertActorFormMarkupReset(t, list.Body.String(), production.ID)
}

func assertActorFormIsReset(t *testing.T, handler *ActorHandler, productionID int64) {
	t.Helper()
	list := httptest.NewRecorder()
	if err := handler.ListActors(list, httptest.NewRequest(http.MethodGet, productionActorsPath(productionID), nil)); err != nil {
		t.Fatal(err)
	}
	assertActorFormMarkupReset(t, list.Body.String(), productionID)
}

func assertActorFormMarkupReset(t *testing.T, body string, productionID int64) {
	t.Helper()
	action := `/production/` + strconvFormat(productionID) + `/actors`
	if !strings.Contains(body, `action="`+action+`"`) || !strings.Contains(body, `hx-post="`+action+`"`) || !strings.Contains(body, ">Add actor</button>") {
		t.Fatalf("actor form was not reset: %s", body)
	}
	if strings.Contains(body, ">Save actor</button>") {
		t.Fatalf("actor form still targets edit mode: %s", body)
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
