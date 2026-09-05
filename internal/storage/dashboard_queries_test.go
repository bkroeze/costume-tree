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
		{ProductionID: production.ID, ActorID: ada.ID, ItemTypeID: typ.ID, Status: StatusReady, Progress: 20},
		{ProductionID: production.ID, ActorID: ada.ID, ItemTypeID: typ.ID, Status: StatusInProgress, Progress: 80},
		{ProductionID: production.ID, ActorID: ada.ID, ItemTypeID: typ.ID, Status: StatusComplete, Progress: 100},
		{ProductionID: production.ID, ActorID: archivedActor.ID, ItemTypeID: typ.ID, Status: StatusBlocked, Progress: 0},
	} {
		if _, err := items.Create(ctx, input); err != nil {
			t.Fatal(err)
		}
	}
	archivedItem, err := items.Create(ctx, CreateCostumeItemInput{ProductionID: production.ID, ActorID: ada.ID, ItemTypeID: typ.ID, Status: StatusBlocked, Progress: 0})
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
	if kpis.TotalActivePieces != 3 || kpis.Ready != 1 || kpis.Complete != 1 || kpis.InProgress != 1 || kpis.Blocked != 0 || kpis.ReadyComplete != 2 || kpis.Incomplete != 2 {
		t.Fatalf("kpis = %#v", kpis)
	}
	summaries, err := queries.ActorSummaries(ctx, production.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 || summaries[0].Name != "Ada" {
		t.Fatalf("summaries = %#v", summaries)
	}
	if summaries[0].ActiveItems != 3 || summaries[0].ReadyComplete != 2 || summaries[0].Incomplete != 2 || summaries[0].Completion == nil || *summaries[0].Completion != 200.0/3.0 {
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
