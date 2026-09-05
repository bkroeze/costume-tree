package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func createPhotoTestItem(t *testing.T, ctx context.Context, db *DB, productionName string) (Production, CostumeItem) {
	t.Helper()
	production, err := NewProductionRepository(db).Create(ctx, CreateProductionInput{Name: productionName})
	if err != nil {
		t.Fatal(err)
	}
	actor, err := NewActorRepository(db).Create(ctx, CreateActorInput{ProductionID: production.ID, Name: "Actor"})
	if err != nil {
		t.Fatal(err)
	}
	itemType, err := NewItemTypeRepository(db).Create(ctx, CreateItemTypeInput{ProductionID: production.ID, Name: "Costume"})
	if err != nil {
		t.Fatal(err)
	}
	item, err := NewCostumeItemRepository(db).Create(ctx, CreateCostumeItemInput{
		ProductionID: production.ID,
		ActorID:      actor.ID,
		ItemTypeID:   itemType.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	return production, item
}

func validPhotoInput(productionID, itemID int64, suffix string) CreateCostumeItemPhotoInput {
	return CreateCostumeItemPhotoInput{
		ProductionID:  productionID,
		CostumeItemID: itemID,
		OriginalName:  "source-" + suffix + ".jpg",
		DisplayName:   "display-" + suffix + ".webp",
		ThumbnailName: "thumbnail-" + suffix + ".webp",
		MediaType:     "image/jpeg",
	}
}

func TestPhotoRepositoryScopesReadsAndOrdersLists(t *testing.T) {
	db, ctx := openTestDB(t)
	firstProduction, firstItem := createPhotoTestItem(t, ctx, db, "First")
	secondProduction, secondItem := createPhotoTestItem(t, ctx, db, "Second")
	photos := NewCostumeItemPhotoRepository(db)

	first, err := photos.Create(ctx, validPhotoInput(firstProduction.ID, firstItem.ID, "one"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := photos.Create(ctx, validPhotoInput(firstProduction.ID, firstItem.ID, "two"))
	if err != nil {
		t.Fatal(err)
	}
	other, err := photos.Create(ctx, validPhotoInput(secondProduction.ID, secondItem.ID, "other"))
	if err != nil {
		t.Fatal(err)
	}

	listed, err := photos.List(ctx, firstProduction.ID, firstItem.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 || listed[0].ID != first.ID || listed[1].ID != second.ID {
		t.Fatalf("first item photos = %#v, want IDs %d then %d", listed, first.ID, second.ID)
	}
	if _, err := photos.Get(ctx, secondProduction.ID, secondItem.ID, first.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-scope Get error = %v, want ErrNotFound", err)
	}
	otherListed, err := photos.List(ctx, secondProduction.ID, secondItem.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(otherListed) != 1 || otherListed[0].ID != other.ID {
		t.Fatalf("second item photos = %#v", otherListed)
	}
}

func TestPhotoRepositoryListsFirstReadyPhotoPerActorItem(t *testing.T) {
	db, ctx := openTestDB(t)
	production, firstItem := createPhotoTestItem(t, ctx, db, "Inventory thumbnails")
	actors := NewActorRepository(db)
	types := NewItemTypeRepository(db)
	items := NewCostumeItemRepository(db)
	secondActor, err := actors.Create(ctx, CreateActorInput{ProductionID: production.ID, Name: "Second actor"})
	if err != nil {
		t.Fatal(err)
	}
	itemType, err := types.Create(ctx, CreateItemTypeInput{ProductionID: production.ID, Name: "Hat"})
	if err != nil {
		t.Fatal(err)
	}
	secondItem, err := items.Create(ctx, CreateCostumeItemInput{ProductionID: production.ID, ActorID: secondActor.ID, ItemTypeID: itemType.ID})
	if err != nil {
		t.Fatal(err)
	}
	photos := NewCostumeItemPhotoRepository(db)
	first, err := photos.Create(ctx, validPhotoInput(production.ID, firstItem.ID, "first"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := photos.MarkReady(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	later, err := photos.Create(ctx, validPhotoInput(production.ID, firstItem.ID, "later"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := photos.MarkReady(ctx, later.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := photos.Create(ctx, validPhotoInput(production.ID, firstItem.ID, "pending")); err != nil {
		t.Fatal(err)
	}
	otherActorPhoto, err := photos.Create(ctx, validPhotoInput(production.ID, secondItem.ID, "other-actor"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := photos.MarkReady(ctx, otherActorPhoto.ID); err != nil {
		t.Fatal(err)
	}

	listed, err := photos.ListFirstReadyByActor(ctx, production.ID, firstItem.ActorID)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != first.ID {
		t.Fatalf("first ready photos = %#v, want only photo %d", listed, first.ID)
	}
}

func TestPhotoRepositoryTransitionsPendingRows(t *testing.T) {
	db, ctx := openTestDB(t)
	production, item := createPhotoTestItem(t, ctx, db, "Transitions")
	photos := NewCostumeItemPhotoRepository(db)

	readyInput := validPhotoInput(production.ID, item.ID, "ready")
	readyPhoto, err := photos.Create(ctx, readyInput)
	if err != nil {
		t.Fatal(err)
	}
	if readyPhoto.Status != PhotoStatusPending || readyPhoto.CreatedAt.IsZero() || readyPhoto.UpdatedAt.IsZero() {
		t.Fatalf("created photo = %#v", readyPhoto)
	}
	readyPhoto, err = photos.MarkReady(ctx, readyPhoto.ID)
	if err != nil {
		t.Fatal(err)
	}
	if readyPhoto.Status != PhotoStatusReady || readyPhoto.ErrorMessage != "" || readyPhoto.OriginalName != readyInput.OriginalName {
		t.Fatalf("ready photo = %#v", readyPhoto)
	}
	if _, err := photos.MarkFailed(ctx, readyPhoto.ID, "too late"); !errors.Is(err, ErrInvalidPhotoStatus) {
		t.Fatalf("ready-to-failed error = %v, want ErrInvalidPhotoStatus", err)
	}

	failedInput := validPhotoInput(production.ID, item.ID, "failed")
	failedPhoto, err := photos.Create(ctx, failedInput)
	if err != nil {
		t.Fatal(err)
	}
	failedPhoto, err = photos.MarkFailed(ctx, failedPhoto.ID, "decode failed")
	if err != nil {
		t.Fatal(err)
	}
	if failedPhoto.Status != PhotoStatusFailed || failedPhoto.ErrorMessage != "decode failed" || failedPhoto.OriginalName != failedInput.OriginalName {
		t.Fatalf("failed photo = %#v", failedPhoto)
	}
	if _, err := photos.MarkReady(ctx, failedPhoto.ID); !errors.Is(err, ErrInvalidPhotoStatus) {
		t.Fatalf("failed-to-ready error = %v, want ErrInvalidPhotoStatus", err)
	}
	if _, err := photos.MarkReady(ctx, failedPhoto.ID+1000); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing photo transition error = %v, want ErrNotFound", err)
	}

	pending, err := photos.ListPending(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending photos after transitions = %#v", pending)
	}
}

func TestPhotoRepositoryValidatesMetadata(t *testing.T) {
	db, ctx := openTestDB(t)
	production, item := createPhotoTestItem(t, ctx, db, "Validation")
	photos := NewCostumeItemPhotoRepository(db)

	invalidNames := []string{"", " ", ".", "..", "../source.jpg", "/source.jpg", `folder\\source.jpg`, "bad\x00name.jpg"}
	for _, name := range invalidNames {
		input := validPhotoInput(production.ID, item.ID, "invalid")
		input.OriginalName = name
		if _, err := photos.Create(ctx, input); err == nil {
			t.Errorf("Create with original filename %q returned nil error", name)
		}
	}
	for _, mutate := range []func(*CreateCostumeItemPhotoInput){
		func(input *CreateCostumeItemPhotoInput) { input.DisplayName = "../display.webp" },
		func(input *CreateCostumeItemPhotoInput) { input.ThumbnailName = `folder\\thumbnail.webp` },
		func(input *CreateCostumeItemPhotoInput) { input.MediaType = "  " },
	} {
		input := validPhotoInput(production.ID, item.ID, "invalid-field")
		mutate(&input)
		if _, err := photos.Create(ctx, input); err == nil {
			t.Error("Create with invalid photo metadata returned nil error")
		}
	}

	input := validPhotoInput(production.ID, item.ID, "constraint")
	if _, err := db.SQL().ExecContext(ctx,
		`INSERT INTO costume_item_photos
		 (production_id, costume_item_id, original_name, display_name, thumbnail_name, media_type, status)
		 VALUES (?, ?, ?, ?, ?, ?, 'unknown')`,
		input.ProductionID, input.CostumeItemID, input.OriginalName, input.DisplayName, input.ThumbnailName, input.MediaType,
	); err == nil {
		t.Fatal("database accepted invalid photo status")
	}
}

func TestPendingPhotosRecoverAfterDatabaseRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "photos.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	production, item := createPhotoTestItem(t, ctx, db, "Recovery")
	photos := NewCostumeItemPhotoRepository(db)
	first, err := photos.Create(ctx, validPhotoInput(production.ID, item.ID, "first"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := photos.Create(ctx, validPhotoInput(production.ID, item.ID, "second"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := photos.MarkReady(ctx, second.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	pending, err := NewCostumeItemPhotoRepository(reopened).ListPending(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].ID != first.ID || pending[0].Status != PhotoStatusPending {
		t.Fatalf("recovered pending photos = %#v", pending)
	}
}

func TestPhotoMigrationUpgradesInitialSchemaAndIsValidated(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "upgrade.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	initial, err := migrations.ReadFile("migrations/0001_initial.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().ExecContext(ctx, `CREATE TABLE schema_migrations (
		version INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		applied_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().ExecContext(ctx, string(initial)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().ExecContext(ctx,
		`INSERT INTO schema_migrations (version, name) VALUES (1, '0001_initial.sql')`,
	); err != nil {
		t.Fatal(err)
	}

	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("upgrade from 0001: %v", err)
	}
	if err := db.Ready(ctx); err != nil {
		t.Fatalf("Ready after photo migration: %v", err)
	}
	var migrationName string
	if err := db.SQL().QueryRowContext(ctx, `SELECT name FROM schema_migrations WHERE version = 2`).Scan(&migrationName); err != nil {
		t.Fatal(err)
	}
	if migrationName != "0002_costume_item_photos.sql" {
		t.Fatalf("migration 2 name = %q", migrationName)
	}

	if _, err := db.SQL().ExecContext(ctx, `DROP INDEX idx_costume_item_photos_pending`); err != nil {
		t.Fatal(err)
	}
	if err := db.Ready(ctx); !errors.Is(err, ErrInvalidDatabase) {
		t.Fatalf("Ready after dropping photo index = %v, want ErrInvalidDatabase", err)
	}
}
