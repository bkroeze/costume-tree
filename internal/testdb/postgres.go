package testdb

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

type TB interface {
	Helper()
	Cleanup(func())
	Fatalf(string, ...any)
}

var schemaSequence atomic.Uint64

func OpenSchema(t TB) (string, context.Context) {
	t.Helper()
	connectionString := os.Getenv("PG_TEST_URL")
	if connectionString == "" {
		t.Fatalf("PG_TEST_URL must be set for database-backed tests")
	}
	ctx := context.Background()
	config, err := pgx.ParseConfig(connectionString)
	if err != nil {
		t.Fatalf("parse PG_TEST_URL: %v", err)
	}
	admin := stdlib.OpenDB(*config)
	if err := admin.PingContext(ctx); err != nil {
		_ = admin.Close()
		t.Fatalf("open PG_TEST_URL: %v", err)
	}
	schema := fmt.Sprintf("costume_tree_test_%d_%d_%d", os.Getpid(), time.Now().UnixNano(), schemaSequence.Add(1))
	if _, err := admin.ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		_ = admin.Close()
		t.Fatalf("create test schema: %v", err)
	}
	isolatedConfig := config.Copy()
	if isolatedConfig.RuntimeParams == nil {
		isolatedConfig.RuntimeParams = make(map[string]string)
	}
	isolatedConfig.RuntimeParams["search_path"] = schema
	isolatedConnectionString := stdlib.RegisterConnConfig(isolatedConfig)
	t.Cleanup(func() {
		stdlib.UnregisterConnConfig(isolatedConnectionString)
		_, _ = admin.ExecContext(context.Background(), `DROP SCHEMA `+schema+` CASCADE`)
		_ = admin.Close()
	})
	return isolatedConnectionString, ctx
}
