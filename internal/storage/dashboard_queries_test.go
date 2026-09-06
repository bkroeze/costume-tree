package storage

import "testing"

func TestDashboardQueriesExcludeArchivedAndAverageActiveProgress(t *testing.T) {
	db, ctx := openTestDB(t)
	productions := NewProductionRepository(db)
	actors := NewActorRepository(db)
	types := NewItemTypeRepository(db)
	items := NewCostumeItemRepository(db)
	production, err := productions.Create(ctx, CreateProductionInput{Name: "Macbeth"})
	if err != nil {
		t.Fatal(err)
	}
	ada, err := actors.Create(ctx, CreateActorInput{ProductionID: production.ID, Name: "Ada", Role: "Lead"})
	if err != nil {
		t.Fatal(err)
	}
	archivedActor, err := actors.Create(ctx, CreateActorInput{ProductionID: production.ID, Name: "Archived"})
	if err != nil {
		t.Fatal(err)
	}
	typ, err := types.Create(ctx, CreateItemTypeInput{ProductionID: production.ID, Name: "Cloak"})
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []CreateCostumeItemInput{
		{ProductionID: production.ID, ActorID: ada.ID, ItemTypeID: typ.ID, Status: StatusFind, Progress: 0},
		{ProductionID: production.ID, ActorID: ada.ID, ItemTypeID: typ.ID, Status: StatusMake, Progress: 25, Blocker: "Waiting for fabric"},
		{ProductionID: production.ID, ActorID: ada.ID, ItemTypeID: typ.ID, Status: StatusFit, Progress: 50, Blocker: "   "},
		{ProductionID: production.ID, ActorID: ada.ID, ItemTypeID: typ.ID, Status: StatusAlterations, Progress: 75},
		{ProductionID: production.ID, ActorID: ada.ID, ItemTypeID: typ.ID, Status: StatusComplete, Progress: 100},
		{ProductionID: production.ID, ActorID: archivedActor.ID, ItemTypeID: typ.ID, Status: StatusMake, Progress: 0, Blocker: "Archived actor blocker"},
	} {
		if _, err := items.Create(ctx, input); err != nil {
			t.Fatal(err)
		}
	}
	archivedItem, err := items.Create(ctx, CreateCostumeItemInput{ProductionID: production.ID, ActorID: ada.ID, ItemTypeID: typ.ID, Status: StatusFind, Progress: 0, Blocker: "Archived item blocker"})
	if err != nil {
		t.Fatal(err)
	}
	if err := items.Archive(ctx, production.ID, archivedItem.ID); err != nil {
		t.Fatal(err)
	}
	if err := actors.Archive(ctx, production.ID, archivedActor.ID); err != nil {
		t.Fatal(err)
	}

	queries := NewDashboardQueries(db)
	kpis, err := queries.ProductionKPIs(ctx, production.ID)
	if err != nil {
		t.Fatal(err)
	}
	if kpis.TotalActivePieces != 5 || kpis.Find != 1 || kpis.Make != 1 || kpis.Fit != 1 || kpis.Alterations != 1 || kpis.Complete != 1 || kpis.Blocked != 1 || kpis.Incomplete != 4 {
		t.Fatalf("kpis = %#v", kpis)
	}
	summaries, err := queries.ActorSummaries(ctx, production.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 || summaries[0].Name != "Ada" {
		t.Fatalf("summaries = %#v", summaries)
	}
	if summaries[0].ActiveItems != 5 || summaries[0].Find != 1 || summaries[0].Make != 1 || summaries[0].Fit != 1 || summaries[0].Alterations != 1 || summaries[0].Complete != 1 || summaries[0].Blocked != 1 || summaries[0].Incomplete != 4 || summaries[0].Completion == nil || *summaries[0].Completion != 50 {
		t.Fatalf("actor summary = %#v", summaries[0])
	}
}

func TestDashboardQueriesRepresentActorWithoutItemsAsNilCompletion(t *testing.T) {
	db, ctx := openTestDB(t)
	production, err := NewProductionRepository(db).Create(ctx, CreateProductionInput{Name: "Hamlet"})
	if err != nil {
		t.Fatal(err)
	}
	actor, err := NewActorRepository(db).Create(ctx, CreateActorInput{ProductionID: production.ID, Name: "Ghost"})
	if err != nil {
		t.Fatal(err)
	}
	summaries, err := NewDashboardQueries(db).ActorSummaries(ctx, production.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 || summaries[0].ID != actor.ID || summaries[0].ActiveItems != 0 || summaries[0].Completion != nil {
		t.Fatalf("empty actor summary = %#v", summaries)
	}
}
