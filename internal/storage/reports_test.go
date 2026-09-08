package storage

import (
	"context"
	"path/filepath"
	"testing"
)

func TestReportRepositoryFiltersStatusesAndListsAccessories(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "reports.db"))
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
	reports := NewReportRepository(db)
	production, err := productions.Create(ctx, CreateProductionInput{Name: "Macbeth"})
	if err != nil {
		t.Fatal(err)
	}
	actor, err := actors.Create(ctx, CreateActorInput{ProductionID: production.ID, Name: "Ada"})
	if err != nil {
		t.Fatal(err)
	}
	accessories, err := types.Create(ctx, CreateItemTypeInput{ProductionID: production.ID, Name: "Accessories"})
	if err != nil {
		t.Fatal(err)
	}
	cloak, err := types.Create(ctx, CreateItemTypeInput{ProductionID: production.ID, Name: "Cloak"})
	if err != nil {
		t.Fatal(err)
	}
	create := func(itemTypeID int64, description, status string, progress int) {
		t.Helper()
		if _, err := items.Create(ctx, CreateCostumeItemInput{
			ProductionID: production.ID, ActorID: actor.ID, ItemTypeID: itemTypeID,
			Description: description, Status: status, Progress: progress,
		}); err != nil {
			t.Fatal(err)
		}
	}
	create(accessories.ID, "Red gloves", StatusFit, 50)
	create(accessories.ID, "Hat", StatusMake, 25)
	create(cloak.ID, "Blue velvet", StatusFit, 50)

	fit, err := reports.Generate(ctx, ReportFilter{ProductionID: production.ID, Status: StatusFit})
	if err != nil {
		t.Fatal(err)
	}
	if len(fit.Categories) != 2 || fit.Categories[0].ItemTypeName != "Accessories" || fit.Categories[0].Count != 1 || fit.Categories[1].ItemTypeName != "Cloak" || fit.Categories[1].Count != 1 {
		t.Fatalf("fit categories = %+v", fit.Categories)
	}
	if len(fit.Accessories) != 1 || fit.Accessories[0].Description != "Red gloves" || fit.Accessories[0].ActorName != "Ada" {
		t.Fatalf("fit accessories = %+v", fit.Accessories)
	}

	all, err := reports.Generate(ctx, ReportFilter{ProductionID: production.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Categories) != 2 || all.Categories[0].Count != 2 || all.Categories[1].Count != 1 || len(all.Accessories) != 2 {
		t.Fatalf("all report = %+v", all)
	}
}
