package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	bulkinput "costume-tree/internal/bulk"
)

func bulkStorageFixture(t *testing.T) (*DB, Production, context.Context) {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "bulk.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	production, err := NewProductionRepository(db).Create(ctx, CreateProductionInput{Name: "Macbeth"})
	if err != nil {
		t.Fatal(err)
	}
	return db, production, ctx
}

func TestBulkImportResolvesCaseInsensitiveNamesAndAllocatesRepeatedCodes(t *testing.T) {
	db, production, ctx := bulkStorageFixture(t)
	if _, err := NewActorRepository(db).Create(ctx, CreateActorInput{ProductionID: production.ID, Name: "Ada"}); err != nil {
		t.Fatal(err)
	}
	if _, err := NewItemTypeRepository(db).Create(ctx, CreateItemTypeInput{ProductionID: production.ID, Name: "Cloak"}); err != nil {
		t.Fatal(err)
	}
	blocks, err := bulkinput.Parse("ada\ncloak\ncloak\n\nNew Actor\nHat")
	if err != nil {
		t.Fatal(err)
	}
	importer := NewBulkImporter(db)
	preview, err := importer.Preview(ctx, production.ID, blocks, true)
	if err != nil || !preview.Valid {
		t.Fatalf("preview = %#v, err = %v", preview, err)
	}
	if preview.Rows[0].ActorState != BulkStateExisting || preview.Rows[0].ItemTypeState != BulkStateKnown || !preview.Rows[1].Duplicate {
		t.Fatalf("resolved rows = %#v", preview.Rows[:2])
	}
	items, err := importer.Commit(ctx, preview)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 || items[0].Code != "C-0001" || items[1].Code != "C-0002" || items[2].Code != "C-0003" {
		t.Fatalf("items = %#v", items)
	}
	actors, err := NewActorRepository(db).List(ctx, production.ID)
	if err != nil || len(actors) != 2 {
		t.Fatalf("actors = %#v, err = %v", actors, err)
	}
}

func TestBulkImportUnknownTypeOptInAndDuplicateSubmit(t *testing.T) {
	db, production, ctx := bulkStorageFixture(t)
	blocks, err := bulkinput.Parse("Ada\nUnknown")
	if err != nil {
		t.Fatal(err)
	}
	importer := NewBulkImporter(db)
	withoutOptIn, err := importer.Preview(ctx, production.ID, blocks, false)
	if err != nil || withoutOptIn.Valid || withoutOptIn.Rows[0].ItemTypeState != BulkStateUnknown {
		t.Fatalf("without opt-in = %#v, err = %v", withoutOptIn, err)
	}
	withOptIn, err := importer.Preview(ctx, production.ID, blocks, true)
	if err != nil || !withOptIn.Valid || withOptIn.Rows[0].ItemTypeState != BulkStateNew {
		t.Fatalf("with opt-in = %#v, err = %v", withOptIn, err)
	}
	if _, err := importer.Commit(ctx, withOptIn); err != nil {
		t.Fatal(err)
	}
	if _, err := importer.Commit(ctx, withOptIn); !errors.Is(err, ErrBulkDuplicateSubmit) {
		t.Fatalf("second commit err = %v, want duplicate", err)
	}
}

func TestBulkImportRollbackLeavesNoNewRecords(t *testing.T) {
	db, production, ctx := bulkStorageFixture(t)
	blocks, err := bulkinput.Parse("Rollback Actor\nUnknown")
	if err != nil {
		t.Fatal(err)
	}
	importer := NewBulkImporter(db)
	preview, err := importer.Preview(ctx, production.ID, blocks, true)
	if err != nil || !preview.Valid {
		t.Fatal(err)
	}
	// Simulate a stale policy decision between preview and commit. The actor
	// insertion must roll back when the type is no longer allowed.
	preview.AllowNewTypes = false
	if _, err := importer.Commit(ctx, preview); !errors.Is(err, ErrBulkUnknownType) {
		t.Fatalf("commit err = %v, want unknown type", err)
	}
	actors, err := NewActorRepository(db).List(ctx, production.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(actors) != 0 {
		t.Fatalf("actors after rollback = %#v", actors)
	}
	items, err := NewCostumeItemRepository(db).List(ctx, CostumeItemFilter{ProductionID: production.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("items after rollback = %#v", items)
	}
}
