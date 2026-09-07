package storage

import "testing"

func TestItemSearchFiltersAndProductionScope(t *testing.T) {
	db, ctx := openTestDB(t)
	productions := NewProductionRepository(db)
	actors := NewActorRepository(db)
	types := NewItemTypeRepository(db)
	items := NewCostumeItemRepository(db)
	first, err := productions.Create(ctx, CreateProductionInput{Name: "Macbeth"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := productions.Create(ctx, CreateProductionInput{Name: "Hamlet"})
	if err != nil {
		t.Fatal(err)
	}
	ada, err := actors.Create(ctx, CreateActorInput{ProductionID: first.ID, Name: "Ada", Role: "Lead"})
	if err != nil {
		t.Fatal(err)
	}
	bob, err := actors.Create(ctx, CreateActorInput{ProductionID: first.ID, Name: "Bob", Role: "Tailor"})
	if err != nil {
		t.Fatal(err)
	}
	archived, err := actors.Create(ctx, CreateActorInput{ProductionID: first.ID, Name: "Archived", Role: "Ghost"})
	if err != nil {
		t.Fatal(err)
	}
	cloak, err := types.Create(ctx, CreateItemTypeInput{ProductionID: first.ID, Name: "Cloak"})
	if err != nil {
		t.Fatal(err)
	}
	mask, err := types.Create(ctx, CreateItemTypeInput{ProductionID: first.ID, Name: "Mask"})
	if err != nil {
		t.Fatal(err)
	}
	secondActor, err := actors.Create(ctx, CreateActorInput{ProductionID: second.ID, Name: "Ada"})
	if err != nil {
		t.Fatal(err)
	}
	secondType, err := types.Create(ctx, CreateItemTypeInput{ProductionID: second.ID, Name: "Cloak"})
	if err != nil {
		t.Fatal(err)
	}

	makeItem := func(input CreateCostumeItemInput) CostumeItem {
		item, createErr := items.Create(ctx, input)
		if createErr != nil {
			t.Fatal(createErr)
		}
		return item
	}
	lead := makeItem(CreateCostumeItemInput{ProductionID: first.ID, ActorID: ada.ID, ItemTypeID: cloak.ID, Description: "Blue velvet", Status: StatusFit, Progress: 40, NextAction: "Fit", Blocker: "Need trim"})
	maskItem := makeItem(CreateCostumeItemInput{ProductionID: first.ID, ActorID: bob.ID, ItemTypeID: mask.ID, Description: "Gold mask", Status: StatusMake, Progress: 20, Blocker: "   "})
	complete := makeItem(CreateCostumeItemInput{ProductionID: first.ID, ActorID: bob.ID, ItemTypeID: cloak.ID, Description: "Green velvet", Status: StatusComplete, Progress: 100})
	archivedItem := makeItem(CreateCostumeItemInput{ProductionID: first.ID, ActorID: ada.ID, ItemTypeID: mask.ID, Description: "Archived mask"})
	_ = makeItem(CreateCostumeItemInput{ProductionID: first.ID, ActorID: archived.ID, ItemTypeID: cloak.ID, Description: "Archived actor item"})
	other := makeItem(CreateCostumeItemInput{ProductionID: second.ID, ActorID: secondActor.ID, ItemTypeID: secondType.ID, Description: "Other production"})
	if err := items.Archive(ctx, first.ID, archivedItem.ID); err != nil {
		t.Fatal(err)
	}
	if err := actors.Archive(ctx, first.ID, archived.ID); err != nil {
		t.Fatal(err)
	}
	blockedItems, err := items.List(ctx, CostumeItemFilter{ProductionID: first.ID, Blocked: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(blockedItems) != 1 || blockedItems[0].ID != lead.ID {
		t.Fatalf("blocked repository results = %#v, want workflow item with blocker note", blockedItems)
	}

	search := NewItemSearch(db)
	all, err := search.Search(ctx, ItemSearchFilter{ProductionID: first.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 || all[0].ID != maskItem.ID || all[1].ID != lead.ID || all[2].ID != complete.ID {
		t.Fatalf("active results = %#v", all)
	}

	checks := []struct {
		name   string
		filter ItemSearchFilter
		want   int64
	}{
		{"code", ItemSearchFilter{ProductionID: first.ID, Code: "  " + stringsToLower(lead.Code) + "  "}, lead.ID},
		{"actor", ItemSearchFilter{ProductionID: first.ID, ActorID: ada.ID}, lead.ID},
		{"type", ItemSearchFilter{ProductionID: first.ID, ItemTypeID: mask.ID}, maskItem.ID},
		{"status", ItemSearchFilter{ProductionID: first.ID, Status: StatusMake}, maskItem.ID},
		{"blocked", ItemSearchFilter{ProductionID: first.ID, Blocked: true}, lead.ID},
		{"incomplete", ItemSearchFilter{ProductionID: first.ID, Incomplete: true}, lead.ID},
		{"min", ItemSearchFilter{ProductionID: first.ID, MinProgress: intPtr(50)}, complete.ID},
		{"max", ItemSearchFilter{ProductionID: first.ID, MaxProgress: intPtr(25)}, maskItem.ID},
		{"text role", ItemSearchFilter{ProductionID: first.ID, Text: "tail"}, maskItem.ID},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			got, err := search.Search(ctx, check.filter)
			if err != nil {
				t.Fatal(err)
			}
			if check.name == "incomplete" {
				if len(got) != 2 || got[0].ID != maskItem.ID || got[1].ID != lead.ID {
					t.Fatalf("results = %#v, want incomplete active items", got)
				}
				return
			}
			if check.name == "text role" {
				if len(got) != 2 || got[0].ID != maskItem.ID || got[1].ID != complete.ID {
					t.Fatalf("results = %#v, want Bob's items", got)
				}
				return
			}
			if len(got) != 1 || got[0].ID != check.want {
				t.Fatalf("results = %#v, want %d", got, check.want)
			}
		})
	}
	combined, err := search.Search(ctx, ItemSearchFilter{ProductionID: first.ID, Text: "velvet", Incomplete: true, MinProgress: intPtr(30), MaxProgress: intPtr(50)})
	if err != nil {
		t.Fatal(err)
	}
	if len(combined) != 1 || combined[0].ID != lead.ID {
		t.Fatalf("combined = %#v", combined)
	}
	otherResults, err := search.Search(ctx, ItemSearchFilter{ProductionID: second.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(otherResults) != 1 || otherResults[0].ID != other.ID {
		t.Fatalf("second production = %#v", otherResults)
	}
	archivedResults, err := search.Search(ctx, ItemSearchFilter{ProductionID: first.ID, IncludeArchived: true, Text: "archived"})
	if err != nil {
		t.Fatal(err)
	}
	if len(archivedResults) != 2 || archivedResults[0].ID != archivedItem.ID {
		t.Fatalf("archived = %#v", archivedResults)
	}
}

func TestItemSearchBoundsAndPagination(t *testing.T) {
	db, ctx := openTestDB(t)
	production, err := NewProductionRepository(db).Create(ctx, CreateProductionInput{Name: "Macbeth"})
	if err != nil {
		t.Fatal(err)
	}
	actor, err := NewActorRepository(db).Create(ctx, CreateActorInput{ProductionID: production.ID, Name: "Ada"})
	if err != nil {
		t.Fatal(err)
	}
	typ, err := NewItemTypeRepository(db).Create(ctx, CreateItemTypeInput{ProductionID: production.ID, Name: "Cloak"})
	if err != nil {
		t.Fatal(err)
	}
	items := NewCostumeItemRepository(db)
	for range 7 {
		if _, err := items.Create(ctx, CreateCostumeItemInput{ProductionID: production.ID, ActorID: actor.ID, ItemTypeID: typ.ID}); err != nil {
			t.Fatal(err)
		}
	}
	search := NewItemSearch(db)
	first, err := search.Search(ctx, ItemSearchFilter{ProductionID: production.ID, Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 3 {
		t.Fatalf("first page length = %d", len(first))
	}
	second, err := search.Search(ctx, ItemSearchFilter{ProductionID: production.ID, Limit: 3, Offset: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 3 || second[0].ID == first[0].ID {
		t.Fatalf("second page = %#v", second)
	}
	bounded, err := search.Search(ctx, ItemSearchFilter{ProductionID: production.ID, Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if len(bounded) != 7 {
		t.Fatalf("bounded length = %d", len(bounded))
	}
}

func intPtr(value int) *int { return &value }
func stringsToLower(value string) string {
	result := make([]byte, len(value))
	for i := range value {
		if value[i] >= 'A' && value[i] <= 'Z' {
			result[i] = value[i] + ('a' - 'A')
		} else {
			result[i] = value[i]
		}
	}
	return string(result)
}
