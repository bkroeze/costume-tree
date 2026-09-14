package web

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"costume-tree/internal/storage"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

var webTestSchemaSequence atomic.Uint64

func openWebTestDB(t *testing.T) (*storage.DB, context.Context) {
	t.Helper()
	connectionString := os.Getenv("PG_TEST_URL")
	if connectionString == "" {
		t.Skip("PG_TEST_URL is not set")
	}

	ctx := context.Background()
	admin, err := storage.Open(connectionString)
	if err != nil {
		t.Fatalf("open PG_TEST_URL: %v", err)
	}
	schema := fmt.Sprintf(
		"costume_tree_web_test_%d_%d_%d",
		os.Getpid(), time.Now().UnixNano(), webTestSchemaSequence.Add(1),
	)
	if _, err := admin.SQL().ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		_ = admin.Close()
		t.Fatalf("create test schema: %v", err)
	}

	config, err := pgx.ParseConfig(connectionString)
	if err != nil {
		_, _ = admin.SQL().ExecContext(ctx, `DROP SCHEMA `+schema+` CASCADE`)
		_ = admin.Close()
		t.Fatalf("parse PG_TEST_URL: %v", err)
	}
	config.RuntimeParams["search_path"] = schema
	isolatedConnectionString := stdlib.RegisterConnConfig(config)
	db, err := storage.Open(isolatedConnectionString)
	if err != nil {
		stdlib.UnregisterConnConfig(isolatedConnectionString)
		_, _ = admin.SQL().ExecContext(ctx, `DROP SCHEMA `+schema+` CASCADE`)
		_ = admin.Close()
		t.Fatalf("open isolated test schema: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
		stdlib.UnregisterConnConfig(isolatedConnectionString)
		_, _ = admin.SQL().ExecContext(context.Background(), `DROP SCHEMA `+schema+` CASCADE`)
		_ = admin.Close()
	})

	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	return db, ctx
}
