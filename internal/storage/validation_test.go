package storage

import (
	"errors"
	"testing"
)

func TestReadyRejectsMissingSchemaObjectWithoutReapply(t *testing.T) {
	db, ctx := openTestDB(t)
	if _, err := db.SQL().ExecContext(ctx, `DROP INDEX idx_costume_item_photos_pending`); err != nil {
		t.Fatal(err)
	}
	if err := db.Ready(ctx); !errors.Is(err, ErrInvalidDatabase) {
		t.Fatalf("Ready() error = %v, want ErrInvalidDatabase", err)
	}
	if err := db.Migrate(ctx); !errors.Is(err, ErrInvalidDatabase) {
		t.Fatalf("Migrate() error = %v, want ErrInvalidDatabase", err)
	}
}

func TestMigrateRefusesUntrackedSchema(t *testing.T) {
	db, ctx := openTestSchema(t, false)
	if _, err := db.SQL().ExecContext(ctx, `CREATE TABLE foreign_data (id BIGINT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx); err == nil {
		t.Fatal("Migrate() applied migrations to an untracked schema")
	}
	exists, err := tableExists(ctx, db.SQL(), "schema_migrations")
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("Migrate() created migration history in an untracked schema")
	}
}
