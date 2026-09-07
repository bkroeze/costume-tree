package storage

import "testing"

func TestProductionDirectoryAggregatesActiveRecords(t *testing.T) {
	db, ctx := openTestDB(t)
	productions := NewProductionRepository(db)
	actors := NewActorRepository(db)
	types := NewItemTypeRepository(db)
	items := NewCostumeItemRepository(db)
	macbeth, err := productions.Create(ctx, CreateProductionInput{Name: "Macbeth"})
	if err != nil {
		t.Fatal(err)
	}
	hamlet, err := productions.Create(ctx, CreateProductionInput{Name: "Hamlet"})
	if err != nil {
		t.Fatal(err)
	}
	archivedProduction, err := productions.Create(ctx, CreateProductionInput{Name: "Archived"})
	if err != nil {
		t.Fatal(err)
	}
	actor, err := actors.Create(ctx, CreateActorInput{ProductionID: macbeth.ID, Name: "Ada"})
	if err != nil {
		t.Fatal(err)
	}
	archivedActor, err := actors.Create(ctx, CreateActorInput{ProductionID: macbeth.ID, Name: "Archived actor"})
	if err != nil {
		t.Fatal(err)
	}
	typ, err := types.Create(ctx, CreateItemTypeInput{ProductionID: macbeth.ID, Name: "Cloak"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := items.Create(ctx, CreateCostumeItemInput{ProductionID: macbeth.ID, ActorID: actor.ID, ItemTypeID: typ.ID, Blocker: "Waiting"}); err != nil {
		t.Fatal(err)
	}
	archivedItem, err := items.Create(ctx, CreateCostumeItemInput{ProductionID: macbeth.ID, ActorID: actor.ID, ItemTypeID: typ.ID, Blocker: "Archived"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := items.Create(ctx, CreateCostumeItemInput{ProductionID: macbeth.ID, ActorID: archivedActor.ID, ItemTypeID: typ.ID, Blocker: "Hidden"}); err != nil {
		t.Fatal(err)
	}
	if err := items.Archive(ctx, macbeth.ID, archivedItem.ID); err != nil {
		t.Fatal(err)
	}
	if err := actors.Archive(ctx, macbeth.ID, archivedActor.ID); err != nil {
		t.Fatal(err)
	}
	if err := productions.Archive(ctx, archivedProduction.ID); err != nil {
		t.Fatal(err)
	}

	entries, err := NewProductionDirectoryQueries(db).ProductionDirectory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].ID != hamlet.ID || entries[1].ID != macbeth.ID {
		t.Fatalf("directory entries = %#v", entries)
	}
	if entries[0].ActorCount != 0 || entries[0].PieceCount != 0 || entries[0].BlockedCount != 0 {
		t.Fatalf("empty production entry = %#v", entries[0])
	}
	if entries[1].ActorCount != 1 || entries[1].PieceCount != 1 || entries[1].BlockedCount != 1 {
		t.Fatalf("populated production entry = %#v", entries[1])
	}
}
