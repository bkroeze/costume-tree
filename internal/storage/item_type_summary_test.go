package storage

import (
	"context"
	"path/filepath"
	"testing"
)

func TestItemTypeSummaryListCountsActiveItemsByStatus(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "summary.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	productions := NewProductionRepository(db)
	actors := NewActorRepository(db)
	types := NewItemTypeRepository(db)
	items := NewCostumeItemRepository(db)
	summaries := NewItemTypeSummaryRepository(db)

	production, err := productions.Create(ctx, CreateProductionInput{Name: "Macbeth"})
	if err != nil {
		t.Fatal(err)
	}
	otherProduction, err := productions.Create(ctx, CreateProductionInput{Name: "Hamlet"})
	if err != nil {
		t.Fatal(err)
	}
	actor, err := actors.Create(ctx, CreateActorInput{ProductionID: production.ID, Name: "A"})
	if err != nil {
		t.Fatal(err)
	}
	archivedActor, err := actors.Create(ctx, CreateActorInput{ProductionID: production.ID, Name: "Archived"})
	if err != nil {
		t.Fatal(err)
	}
	cloak, err := types.Create(ctx, CreateItemTypeInput{ProductionID: production.ID, Name: "Cloak"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := types.Create(ctx, CreateItemTypeInput{ProductionID: production.ID, Name: "Empty"}); err != nil {
		t.Fatal(err)
	}
	otherType, err := types.Create(ctx, CreateItemTypeInput{ProductionID: otherProduction.ID, Name: "Cloak"})
	if err != nil {
		t.Fatal(err)
	}
	otherActor, err := actors.Create(ctx, CreateActorInput{ProductionID: otherProduction.ID, Name: "Other"})
	if err != nil {
		t.Fatal(err)
	}

	create := func(actorID int64, typID int64, status string, progress int, blocker string) CostumeItem {
		t.Helper()
		item, err := items.Create(ctx, CreateCostumeItemInput{
			ProductionID: production.ID,
			ActorID:      actorID,
			ItemTypeID:   typID,
			Status:       status,
			Progress:     progress,
			Blocker:      blocker,
		})
		if err != nil {
			t.Fatal(err)
		}
		return item
	}
	create(actor.ID, cloak.ID, StatusFind, 10, "  Need fabric  ")
	create(actor.ID, cloak.ID, StatusMake, 30, "")
	create(actor.ID, cloak.ID, StatusFit, 50, "")
	create(actor.ID, cloak.ID, StatusFit, 60, "   ")
	create(actor.ID, cloak.ID, StatusAlterations, 80, "")
	create(actor.ID, cloak.ID, StatusComplete, 100, "")
	archivedItem := create(actor.ID, cloak.ID, StatusFind, 0, "Archived blocker")
	if err := items.Archive(ctx, production.ID, archivedItem.ID); err != nil {
		t.Fatal(err)
	}
	create(archivedActor.ID, cloak.ID, StatusComplete, 100, "Archived actor blocker")
	if err := actors.Archive(ctx, production.ID, archivedActor.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := items.Create(ctx, CreateCostumeItemInput{ProductionID: otherProduction.ID, ActorID: otherActor.ID, ItemTypeID: otherType.ID, Status: StatusMake, Progress: 30, Blocker: "Other production blocker"}); err != nil {
		t.Fatal(err)
	}

	got, err := summaries.List(ctx, production.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("summary rows = %d, want one represented active type", len(got))
	}
	row := got[0]
	if row.ItemTypeID != cloak.ID || row.ItemTypeName != "Cloak" || row.Total != 6 || row.Find != 1 || row.Make != 1 || row.Fit != 2 || row.Alterations != 1 || row.Complete != 1 || row.Blocked != 1 {
		t.Fatalf("summary row = %+v", row)
	}
	other, err := summaries.List(ctx, otherProduction.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(other) != 1 || other[0].Total != 1 || other[0].Make != 1 || other[0].Blocked != 1 || other[0].ItemTypeID != otherType.ID {
		t.Fatalf("second production summary = %+v", other)
	}
}
