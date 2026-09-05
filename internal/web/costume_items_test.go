package web

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
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
	return NewCostumeItemHandler(productions, actors, types, items, nil, nil), production, actor, typ, ctx
}

type photoUploadCall struct {
	item     storage.CostumeItem
	filename string
}

type fakePhotoService struct {
	validateErr error
	uploadErr   error
	uploads     []photoUploadCall
	photos      []storage.CostumeItemPhoto
	listScopes  [][2]int64
	resolveCall struct {
		productionID int64
		itemID       int64
		photoID      int64
		variant      string
	}
	resolvePhoto storage.CostumeItemPhoto
	resolvePath  string
	resolveErr   error
}

func (f *fakePhotoService) Validate(*multipart.FileHeader) error {
	return f.validateErr
}

func (f *fakePhotoService) Upload(_ context.Context, item storage.CostumeItem, file *multipart.FileHeader) (storage.CostumeItemPhoto, error) {
	f.uploads = append(f.uploads, photoUploadCall{item: item, filename: file.Filename})
	if f.uploadErr != nil {
		return storage.CostumeItemPhoto{}, f.uploadErr
	}
	photo := storage.CostumeItemPhoto{ID: int64(len(f.uploads)), ProductionID: item.ProductionID, CostumeItemID: item.ID, Status: storage.PhotoStatusPending}
	f.photos = append(f.photos, photo)
	return photo, nil
}

func (f *fakePhotoService) List(_ context.Context, productionID, itemID int64) ([]storage.CostumeItemPhoto, error) {
	f.listScopes = append(f.listScopes, [2]int64{productionID, itemID})
	return f.photos, nil
}

func (f *fakePhotoService) Resolve(_ context.Context, productionID, itemID, photoID int64, variant string) (storage.CostumeItemPhoto, string, error) {
	f.resolveCall.productionID = productionID
	f.resolveCall.itemID = itemID
	f.resolveCall.photoID = photoID
	f.resolveCall.variant = variant
	return f.resolvePhoto, f.resolvePath, f.resolveErr
}

func costumeItemMultipartRequest(t *testing.T, method, path string, values url.Values, filename string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for name, entries := range values {
		for _, value := range entries {
			if err := writer.WriteField(name, value); err != nil {
				t.Fatal(err)
			}
		}
	}
	if filename != "" {
		part, err := writer.CreateFormFile("photo", filename)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write([]byte("fake image bytes")); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(method, path, &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return request
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

func TestCostumeItemMultipartCreateAndEditUploadBeforeDetailRedirect(t *testing.T) {
	h, production, actor, typ, ctx := costumeItemFixture(t)
	photos := &fakePhotoService{}
	h.photos = photos
	listPath := costumeItemListPath(production.ID, actor.ID)
	createValues := url.Values{
		"item_type_id": {strconv.FormatInt(typ.ID, 10)},
		"description":  {"Blue cloak"},
		"status":       {storage.StatusNotStarted},
		"progress":     {"0"},
	}
	createRequest := costumeItemMultipartRequest(t, http.MethodPost, listPath, createValues, "cloak.jpg")
	createRequest.Header.Set("HX-Request", "true")
	createResponse := httptest.NewRecorder()
	if err := h.CreateCostumeItem(createResponse, createRequest); err != nil {
		t.Fatal(err)
	}
	items, err := h.items.List(ctx, storage.CostumeItemFilter{ProductionID: production.ID, ActorID: actor.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("created items = %d, want 1", len(items))
	}
	detailPath := costumeItemDetailPath(production.ID, actor.ID, items[0].ID)
	if createResponse.Code != http.StatusNoContent || createResponse.Header().Get("HX-Redirect") != detailPath {
		t.Fatalf("multipart create redirect = %d %q, want 204 %q", createResponse.Code, createResponse.Header().Get("HX-Redirect"), detailPath)
	}
	if len(photos.uploads) != 1 || photos.uploads[0].filename != "cloak.jpg" || photos.uploads[0].item.ID != items[0].ID {
		t.Fatalf("create upload = %#v", photos.uploads)
	}

	detail := httptest.NewRecorder()
	if err := h.DetailCostumeItem(detail, costumeItemRequest(http.MethodGet, detailPath, nil)); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`aria-label="Photo processing"`, `hx-trigger="every 1s"`, detailPath + `/photos`} {
		if !strings.Contains(detail.Body.String(), want) {
			t.Errorf("pending detail missing %q: %s", want, detail.Body.String())
		}
	}

	editValues := url.Values{
		"item_type_id": {strconv.FormatInt(typ.ID, 10)},
		"description":  {"Blue cloak with trim"},
		"status":       {storage.StatusInProgress},
		"progress":     {"25"},
		"updated_at":   {formatUpdatedAt(items[0].UpdatedAt)},
	}
	editResponse := httptest.NewRecorder()
	if err := h.EditCostumeItem(editResponse, costumeItemMultipartRequest(t, http.MethodPost, detailPath+"/edit", editValues, "trim.png")); err != nil {
		t.Fatal(err)
	}
	if editResponse.Code != http.StatusSeeOther || editResponse.Header().Get("Location") != detailPath {
		t.Fatalf("multipart edit redirect = %d %q, want 303 %q", editResponse.Code, editResponse.Header().Get("Location"), detailPath)
	}
	if len(photos.uploads) != 2 || photos.uploads[1].filename != "trim.png" || photos.uploads[1].item.Description != "Blue cloak with trim" {
		t.Fatalf("edit upload = %#v", photos.uploads)
	}
}

func TestCostumeItemPhotoUploadFailureReportsSavedItem(t *testing.T) {
	h, production, actor, typ, ctx := costumeItemFixture(t)
	h.photos = &fakePhotoService{uploadErr: errors.New("disk full")}
	listPath := costumeItemListPath(production.ID, actor.ID)
	values := url.Values{
		"item_type_id": {strconv.FormatInt(typ.ID, 10)},
		"description":  {"Saved without photo"},
		"status":       {storage.StatusNotStarted},
		"progress":     {"0"},
	}
	response := httptest.NewRecorder()
	if err := h.CreateCostumeItem(response, costumeItemMultipartRequest(t, http.MethodPost, listPath, values, "photo.jpg")); err != nil {
		t.Fatal(err)
	}
	items, err := h.items.List(ctx, storage.CostumeItemFilter{ProductionID: production.ID, ActorID: actor.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("created items = %d, want saved item", len(items))
	}
	location := costumeItemDetailPath(production.ID, actor.ID, items[0].ID) + "?photo=failed"
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != location {
		t.Fatalf("upload failure response = %d %q, want 303 %q", response.Code, response.Header().Get("Location"), location)
	}
	detail := httptest.NewRecorder()
	if err := h.DetailCostumeItem(detail, costumeItemRequest(http.MethodGet, location, nil)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(detail.Body.String(), "Costume item saved, but the photo could not be attached.") {
		t.Fatalf("detail does not report attachment-only failure: %s", detail.Body.String())
	}
}

func TestCostumeItemPhotoValidationPrecedesCreateAndEditMutation(t *testing.T) {
	h, production, actor, typ, ctx := costumeItemFixture(t)
	photos := &fakePhotoService{validateErr: errors.New("invalid image")}
	h.photos = photos
	listPath := costumeItemListPath(production.ID, actor.ID)
	values := url.Values{
		"item_type_id": {strconv.FormatInt(typ.ID, 10)},
		"description":  {"Must not be created"},
		"status":       {storage.StatusNotStarted},
		"progress":     {"0"},
	}
	response := httptest.NewRecorder()
	if err := h.CreateCostumeItem(response, costumeItemMultipartRequest(t, http.MethodPost, listPath, values, "bad.txt")); err != nil {
		t.Fatal(err)
	}
	items, err := h.items.List(ctx, storage.CostumeItemFilter{ProductionID: production.ID, ActorID: actor.ID})
	if err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusUnprocessableEntity || len(items) != 0 || len(photos.uploads) != 0 {
		t.Fatalf("invalid create = status %d, items %d, uploads %d", response.Code, len(items), len(photos.uploads))
	}

	item, err := h.items.Create(ctx, storage.CreateCostumeItemInput{ProductionID: production.ID, ActorID: actor.ID, ItemTypeID: typ.ID, Description: "Original"})
	if err != nil {
		t.Fatal(err)
	}
	editValues := url.Values{
		"item_type_id": {strconv.FormatInt(typ.ID, 10)},
		"description":  {"Mutated"},
		"status":       {storage.StatusInProgress},
		"progress":     {"20"},
		"updated_at":   {formatUpdatedAt(item.UpdatedAt)},
	}
	response = httptest.NewRecorder()
	if err := h.EditCostumeItem(response, costumeItemMultipartRequest(t, http.MethodPost, costumeItemDetailPath(production.ID, actor.ID, item.ID)+"/edit", editValues, "bad.gif")); err != nil {
		t.Fatal(err)
	}
	stored, err := h.items.Get(ctx, production.ID, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusUnprocessableEntity || stored.Description != "Original" || len(photos.uploads) != 0 {
		t.Fatalf("invalid edit = status %d, description %q, uploads %d", response.Code, stored.Description, len(photos.uploads))
	}
}

func TestCostumeItemPhotoGalleryRendersReadyAndFailedStates(t *testing.T) {
	h, production, actor, typ, ctx := costumeItemFixture(t)
	item, err := h.items.Create(ctx, storage.CreateCostumeItemInput{ProductionID: production.ID, ActorID: actor.ID, ItemTypeID: typ.ID})
	if err != nil {
		t.Fatal(err)
	}
	photos := &fakePhotoService{photos: []storage.CostumeItemPhoto{
		{ID: 7, ProductionID: production.ID, CostumeItemID: item.ID, Status: storage.PhotoStatusReady},
		{ID: 8, ProductionID: production.ID, CostumeItemID: item.ID, Status: storage.PhotoStatusFailed, ErrorMessage: "decode failed"},
	}}
	h.photos = photos
	path := costumeItemDetailPath(production.ID, actor.ID, item.ID)
	response := httptest.NewRecorder()
	if err := h.CostumeItemPhotoGallery(response, costumeItemRequest(http.MethodGet, path+"/photos", nil)); err != nil {
		t.Fatal(err)
	}
	body := response.Body.String()
	for _, want := range []string{
		path + `/photos/7/thumbnail`,
		path + `/photos/7/display`,
		path + `/photos/7/original`,
		`Photo processing failed.`,
		`Choose the photo again in Edit item to retry.`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("gallery missing %q: %s", want, body)
		}
	}
	if strings.Contains(body, `hx-trigger="every 1s"`) {
		t.Errorf("settled gallery unexpectedly polls: %s", body)
	}
}

func TestCostumeItemPhotoServingIsFullyScoped(t *testing.T) {
	h, production, actor, typ, ctx := costumeItemFixture(t)
	item, err := h.items.Create(ctx, storage.CreateCostumeItemInput{ProductionID: production.ID, ActorID: actor.ID, ItemTypeID: typ.ID})
	if err != nil {
		t.Fatal(err)
	}
	otherActor, err := h.actors.Create(ctx, storage.CreateActorInput{ProductionID: production.ID, Name: "Bea"})
	if err != nil {
		t.Fatal(err)
	}
	filePath := filepath.Join(t.TempDir(), "photo.jpg")
	if err := os.WriteFile(filePath, []byte("served photo"), 0o600); err != nil {
		t.Fatal(err)
	}
	photos := &fakePhotoService{
		resolvePhoto: storage.CostumeItemPhoto{ID: 9, ProductionID: production.ID, CostumeItemID: item.ID, MediaType: "image/jpeg", Status: storage.PhotoStatusReady},
		resolvePath:  filePath,
	}
	h.photos = photos
	base := costumeItemDetailPath(production.ID, actor.ID, item.ID) + "/photos/9/"
	for _, variant := range []string{"original", "display", "thumbnail"} {
		response := httptest.NewRecorder()
		if err := h.ServeCostumeItemPhoto(response, costumeItemRequest(http.MethodGet, base+variant, nil)); err != nil {
			t.Fatal(err)
		}
		if response.Code != http.StatusOK || response.Body.String() != "served photo" {
			t.Fatalf("%s response = %d %q", variant, response.Code, response.Body.String())
		}
		if response.Header().Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(response.Header().Get("Cache-Control"), "immutable") {
			t.Errorf("%s security/cache headers = %#v", variant, response.Header())
		}
		if photos.resolveCall.productionID != production.ID || photos.resolveCall.itemID != item.ID || photos.resolveCall.photoID != 9 || photos.resolveCall.variant != variant {
			t.Fatalf("%s resolve scope = %#v", variant, photos.resolveCall)
		}
	}

	wrongActorPath := costumeItemDetailPath(production.ID, otherActor.ID, item.ID) + "/photos/9/display"
	err = h.ServeCostumeItemPhoto(httptest.NewRecorder(), costumeItemRequest(http.MethodGet, wrongActorPath, nil))
	var httpErr *Error
	if !errors.As(err, &httpErr) || httpErr.Status != http.StatusNotFound {
		t.Fatalf("cross-actor photo error = %v, want 404", err)
	}
}
