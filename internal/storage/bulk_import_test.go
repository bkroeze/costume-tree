package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	bulkinput "costume-tree/internal/bulk"
)

func bulkStorageFixture(t *testing.T) (*DB, Production, context.Context) {
	t.Helper()
	db, ctx := openTestDB(t)
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

func TestConcurrentBulkImportsSerializeProductionScope(t *testing.T) {
	db, production, ctx := bulkStorageFixture(t)
	if _, err := NewItemTypeRepository(db).Create(ctx, CreateItemTypeInput{ProductionID: production.ID, Name: "Cloak"}); err != nil {
		t.Fatal(err)
	}
	blocks, err := bulkinput.Parse("Concurrent Actor\nCloak")
	if err != nil {
		t.Fatal(err)
	}
	firstImporter := NewBulkImporter(db)
	secondImporter := NewBulkImporter(db)
	first, err := firstImporter.Preview(ctx, production.ID, blocks, false)
	if err != nil || !first.Valid {
		t.Fatalf("first preview = %#v, err = %v", first, err)
	}
	second, err := secondImporter.Preview(ctx, production.ID, blocks, false)
	if err != nil || !second.Valid {
		t.Fatalf("second preview = %#v, err = %v", second, err)
	}

	blocker, err := db.SQL().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback()
	var lockedID int64
	var blockerPID int
	if err := blocker.QueryRowContext(ctx, `SELECT id, pg_backend_pid() FROM productions WHERE id = $1 FOR UPDATE`, production.ID).Scan(&lockedID, &blockerPID); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	for _, commit := range []func() ([]CostumeItem, error){
		func() ([]CostumeItem, error) { return firstImporter.Commit(ctx, first) },
		func() ([]CostumeItem, error) { return secondImporter.Commit(ctx, second) },
	} {
		go func(commit func() ([]CostumeItem, error)) {
			<-start
			_, err := commit()
			results <- err
		}(commit)
	}
	close(start)
	contentionDeadline := time.NewTimer(5 * time.Second)
	contentionPoll := time.NewTicker(10 * time.Millisecond)
	defer contentionDeadline.Stop()
	defer contentionPoll.Stop()
	for {
		var waiters int
		if err := db.SQL().QueryRowContext(ctx, `
			SELECT count(*)
			FROM pg_stat_activity
			WHERE datname = current_database()
			  AND $1 = ANY(pg_blocking_pids(pid))`, blockerPID).Scan(&waiters); err != nil {
			t.Fatal(err)
		}
		if waiters > 0 {
			break
		}
		select {
		case err := <-results:
			t.Fatalf("bulk import completed without waiting on the production lock: %v", err)
		case <-contentionPoll.C:
		case <-contentionDeadline.C:
			t.Fatal("bulk imports did not contend on the production lock")
		}
	}

	if _, err := blocker.ExecContext(ctx, `INSERT INTO actors (production_id, name) VALUES ($1, $2)`, production.ID, "Concurrent Actor"); err != nil {
		t.Fatal(err)
	}
	if err := blocker.Commit(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		select {
		case err := <-results:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent bulk import did not complete")
		}
	}
	actors, err := NewActorRepository(db).List(ctx, production.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(actors) != 1 || actors[0].Name != "Concurrent Actor" {
		t.Fatalf("actors = %#v, want one shared actor", actors)
	}
}
