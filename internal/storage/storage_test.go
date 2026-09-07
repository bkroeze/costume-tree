package storage

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"testing"
)

func openTestDB(t *testing.T) (*DB, context.Context) {
	t.Helper()
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "costume-tree.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	return db, ctx
}

func TestMigratePersistsSchemaAndRows(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "costume-tree.db")
	ctx := context.Background()

	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	production, err := NewProductionRepository(db).Create(ctx, CreateProductionInput{Name: "Macbeth"})
	if err != nil {
		t.Fatal(err)
	}
	if production.CreatedAt.IsZero() || production.UpdatedAt.IsZero() {
		t.Fatal("expected production timestamps")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	productions, err := NewProductionRepository(db).List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(productions) != 1 || productions[0].Name != "Macbeth" {
		t.Fatalf("persisted productions = %#v", productions)
	}
	var migrationCount int
	if err := db.SQL().QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&migrationCount); err != nil {
		t.Fatal(err)
	}
	if migrationCount != 2 {
		t.Fatalf("migration count = %d, want 2", migrationCount)
	}
}

func TestForeignKeysAndProgressConstraints(t *testing.T) {
	db, ctx := openTestDB(t)
	production, err := NewProductionRepository(db).Create(ctx, CreateProductionInput{Name: "Macbeth"})
	if err != nil {
		t.Fatal(err)
	}
	actor, err := NewActorRepository(db).Create(ctx, CreateActorInput{ProductionID: production.ID, Name: "Banquo"})
	if err != nil {
		t.Fatal(err)
	}
	itemType, err := NewItemTypeRepository(db).Create(ctx, CreateItemTypeInput{ProductionID: production.ID, Name: "Cloak"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.SQL().ExecContext(ctx, `INSERT INTO costume_items (production_id, actor_id, item_type_id, code, progress) VALUES (?, ?, ?, 'C-9999', 101)`, production.ID, actor.ID, itemType.ID)
	if err == nil {
		t.Fatal("expected progress check failure")
	}
	_, err = db.SQL().ExecContext(ctx, `INSERT INTO costume_items (production_id, actor_id, item_type_id, code, status, progress) VALUES (?, ?, ?, 'C-9998', 'Complete', 99)`, production.ID, actor.ID, itemType.ID)
	if err == nil {
		t.Fatal("expected Complete progress check failure")
	}
	_, err = db.SQL().ExecContext(ctx, `INSERT INTO costume_items (production_id, actor_id, item_type_id, code, status, progress) VALUES (?, ?, ?, 'C-9997', 'Find', 0)`, production.ID, actor.ID+1000, itemType.ID)
	if err == nil {
		t.Fatal("expected actor foreign-key failure")
	}
	item, err := NewCostumeItemRepository(db).Create(ctx, CreateCostumeItemInput{
		ProductionID: production.ID,
		ActorID:      actor.ID,
		ItemTypeID:   itemType.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if item.Status != StatusFind {
		t.Fatalf("default costume item status = %q, want %q", item.Status, StatusFind)
	}
	if _, err := db.SQL().ExecContext(ctx, `DELETE FROM actors WHERE id = ?`, actor.ID); err == nil {
		t.Fatal("expected referenced actor delete failure")
	}
}

func TestCostumeItemListOrdersByWorkflowStatusThenID(t *testing.T) {
	db, ctx := openTestDB(t)
	production, err := NewProductionRepository(db).Create(ctx, CreateProductionInput{Name: "Macbeth"})
	if err != nil {
		t.Fatal(err)
	}
	actor, err := NewActorRepository(db).Create(ctx, CreateActorInput{ProductionID: production.ID, Name: "Banquo"})
	if err != nil {
		t.Fatal(err)
	}
	itemType, err := NewItemTypeRepository(db).Create(ctx, CreateItemTypeInput{ProductionID: production.ID, Name: "Cloak"})
	if err != nil {
		t.Fatal(err)
	}
	items := NewCostumeItemRepository(db)
	inputs := []CreateCostumeItemInput{
		{ProductionID: production.ID, ActorID: actor.ID, ItemTypeID: itemType.ID, Status: StatusComplete, Progress: 100},
		{ProductionID: production.ID, ActorID: actor.ID, ItemTypeID: itemType.ID, Status: StatusFind},
		{ProductionID: production.ID, ActorID: actor.ID, ItemTypeID: itemType.ID, Status: StatusFit},
		{ProductionID: production.ID, ActorID: actor.ID, ItemTypeID: itemType.ID, Status: StatusMake},
		{ProductionID: production.ID, ActorID: actor.ID, ItemTypeID: itemType.ID, Status: StatusAlterations},
	}
	for _, input := range inputs {
		if _, err := items.Create(ctx, input); err != nil {
			t.Fatal(err)
		}
	}
	list, err := items.List(ctx, CostumeItemFilter{ProductionID: production.ID})
	if err != nil {
		t.Fatal(err)
	}
	wantStatuses := []string{StatusFind, StatusMake, StatusFit, StatusAlterations, StatusComplete}
	if len(list) != len(wantStatuses) {
		t.Fatalf("listed items = %d, want %d", len(list), len(wantStatuses))
	}
	for index, status := range wantStatuses {
		if list[index].Status != status || list[index].ID != int64([]int{2, 4, 3, 5, 1}[index]) {
			t.Fatalf("list[%d] = %#v, want id/status %d/%s", index, list[index], []int{2, 4, 3, 5, 1}[index], status)
		}
	}
}

func TestArchiveFiltersActiveRows(t *testing.T) {
	db, ctx := openTestDB(t)
	production, err := NewProductionRepository(db).Create(ctx, CreateProductionInput{Name: "Macbeth"})
	if err != nil {
		t.Fatal(err)
	}
	actor, err := NewActorRepository(db).Create(ctx, CreateActorInput{ProductionID: production.ID, Name: "Banquo"})
	if err != nil {
		t.Fatal(err)
	}
	itemType, err := NewItemTypeRepository(db).Create(ctx, CreateItemTypeInput{ProductionID: production.ID, Name: "Cloak"})
	if err != nil {
		t.Fatal(err)
	}
	items := NewCostumeItemRepository(db)
	item, err := items.Create(ctx, CreateCostumeItemInput{ProductionID: production.ID, ActorID: actor.ID, ItemTypeID: itemType.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := items.Archive(ctx, production.ID, item.ID); err != nil {
		t.Fatal(err)
	}
	active, err := items.List(ctx, CostumeItemFilter{ProductionID: production.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 0 {
		t.Fatalf("active items = %#v", active)
	}
	archived, err := items.List(ctx, CostumeItemFilter{ProductionID: production.ID, IncludeArchived: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(archived) != 1 || archived[0].ArchivedAt == nil {
		t.Fatalf("archived items = %#v", archived)
	}
}

func TestConcurrentCodeAllocationIsUniqueAndMonotonic(t *testing.T) {
	db, ctx := openTestDB(t)
	production, err := NewProductionRepository(db).Create(ctx, CreateProductionInput{Name: "Macbeth"})
	if err != nil {
		t.Fatal(err)
	}
	items := NewCostumeItemRepository(db)
	const workers = 24
	codes := make(chan string, workers)
	errs := make(chan error, workers)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			code, err := items.AllocateCode(ctx, production.ID)
			if err != nil {
				errs <- err
				return
			}
			codes <- code
		}()
	}
	group.Wait()
	close(codes)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	got := make([]string, 0, workers)
	for code := range codes {
		got = append(got, code)
	}
	sort.Strings(got)
	want := make([]string, workers)
	for i := range want {
		want[i] = fmt.Sprintf("C-%04d", i+1)
	}
	if len(got) != workers {
		t.Fatalf("allocated %d codes, want %d", len(got), workers)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("codes = %v, want %v", got, want)
		}
	}
}

func TestNotFoundAndClosedDatabaseErrors(t *testing.T) {
	db, ctx := openTestDB(t)
	_, err := NewProductionRepository(db).Get(ctx, 99)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("get missing production error = %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := NewProductionRepository(db).List(ctx); err == nil {
		t.Fatal("expected closed database error")
	}
}
