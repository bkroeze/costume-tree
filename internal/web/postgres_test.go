package web

import (
	"context"
	"testing"

	"costume-tree/internal/storage"
	"costume-tree/internal/testdb"
)

func openWebTestDB(t *testing.T) (*storage.DB, context.Context) {
	t.Helper()
	isolatedConnectionString, ctx := testdb.OpenSchema(t)
	db, err := storage.Open(isolatedConnectionString, 4)
	if err != nil {
		t.Fatalf("open isolated test schema: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	return db, ctx
}
