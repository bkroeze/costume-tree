package web

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"costume-tree/internal/storage"
)

type failingDirectoryQueries struct {
	err error
}

func (q failingDirectoryQueries) ProductionDirectory(context.Context) ([]storage.ProductionDirectoryEntry, error) {
	return nil, q.err
}

func TestLandingPageHeroAndShowsDirectory(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "costume-tree.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	prodRepo := storage.NewProductionRepository(db)
	actorRepo := storage.NewActorRepository(db)
	itemRepo := storage.NewCostumeItemRepository(db)
	typeRepo := storage.NewItemTypeRepository(db)

	// Create 2 test shows
	macbeth, err := prodRepo.Create(ctx, storage.CreateProductionInput{Name: "Macbeth"})
	if err != nil {
		t.Fatal(err)
	}
	hamlet, err := prodRepo.Create(ctx, storage.CreateProductionInput{Name: "Hamlet"})
	if err != nil {
		t.Fatal(err)
	}

	// Add actors to Macbeth
	ladyM, err := actorRepo.Create(ctx, storage.CreateActorInput{ProductionID: macbeth.ID, Name: "Lady Macbeth"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = actorRepo.Create(ctx, storage.CreateActorInput{ProductionID: macbeth.ID, Name: "Banquo"})
	if err != nil {
		t.Fatal(err)
	}

	// Add item type & costume item to Macbeth
	gownType, err := typeRepo.Create(ctx, storage.CreateItemTypeInput{ProductionID: macbeth.ID, Name: "Gown"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = itemRepo.Create(ctx, storage.CreateCostumeItemInput{
		ProductionID: macbeth.ID,
		ActorID:      ladyM.ID,
		ItemTypeID:   gownType.ID,
		Description:  "Blood red velvet gown",
		Status:       storage.StatusFit,
		Progress:     50,
		Blocker:      "Awaiting hem pins",
	})
	if err != nil {
		t.Fatal(err)
	}

	handler, err := New(slog.New(slog.NewTextHandler(io.Discard, nil)), Dependencies{Readiness: db})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET / returned status %d, want %d", rec.Code, http.StatusOK)
	}

	body := rec.Body.String()

	// 1. Verify Option B Hero elements
	for _, want := range []string{
		`class="hero-showcase"`,
		`class="hero-showcase__tag"`,
		`Fast, phone-friendly costume tracking for stage productions.`,
		`/assets/costume-tree-hero.png`,
		`class="hero-showcase__frame"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("landing page missing hero element %q", want)
		}
	}

	// 2. Verify Shows Directory cards
	for _, want := range []string{
		`id="shows-directory"`,
		`Macbeth`,
		`Hamlet`,
		`href="/production/new"`,
		`/production/` + strconv.FormatInt(macbeth.ID, 10) + `/dashboard`,
		`/production/` + strconv.FormatInt(macbeth.ID, 10) + `/actors`,
		`/production/` + strconv.FormatInt(hamlet.ID, 10) + `/dashboard`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("landing page missing show directory element %q", want)
		}
	}

	actorList := httptest.NewRequest(http.MethodGet, "/production/"+strconv.FormatInt(macbeth.ID, 10)+"/actors", nil)
	actorListResponse := httptest.NewRecorder()
	handler.ServeHTTP(actorListResponse, actorList)
	if actorListResponse.Code != http.StatusOK || !strings.Contains(actorListResponse.Body.String(), "Banquo") || !strings.Contains(actorListResponse.Body.String(), "Add an actor") {
		t.Fatalf("production actor list = %d %s", actorListResponse.Code, actorListResponse.Body.String())
	}

	newProduction := httptest.NewRecorder()
	handler.ServeHTTP(newProduction, httptest.NewRequest(http.MethodGet, "/production/new", nil))
	if newProduction.Code != http.StatusOK || !strings.Contains(newProduction.Body.String(), "Name your production") {
		t.Fatalf("new production page = %d %s", newProduction.Code, newProduction.Body.String())
	}

}

func TestLandingPropagatesDirectoryQueryFailures(t *testing.T) {
	sentinel := errors.New("query failed")
	handler := NewLandingHandler(failingDirectoryQueries{err: sentinel}, nil)
	err := handler.Landing(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if !errors.Is(err, sentinel) {
		t.Fatalf("Landing() error = %v, want directory query failure", err)
	}
}
