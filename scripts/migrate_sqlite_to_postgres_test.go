package scripts

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func TestSQLiteMigrationInitialLoadSafeRerunAndLeastPrivilege(t *testing.T) {
	connectionString := os.Getenv("PG_TEST_URL")
	if connectionString == "" {
		t.Fatal("PG_TEST_URL must be set for the SQLite migration test")
	}
	config, err := pgx.ParseConfig(connectionString)
	if err != nil {
		t.Fatalf("parse PG_TEST_URL: %v", err)
	}
	if config.Password == "" {
		t.Fatal("PG_TEST_URL must include a password for the SQLite migration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin := stdlib.OpenDB(*config)
	t.Cleanup(func() { _ = admin.Close() })
	if err := admin.PingContext(ctx); err != nil {
		t.Fatalf("open PG_TEST_URL: %v", err)
	}

	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	databaseName := "costume_tree_migration_" + suffix
	roleName := "costume_tree_reader_" + suffix
	databaseIdentifier := pgx.Identifier{databaseName}.Sanitize()
	roleIdentifier := pgx.Identifier{roleName}.Sanitize()
	if _, err := admin.ExecContext(ctx, `CREATE DATABASE `+databaseIdentifier); err != nil {
		t.Fatalf("create migration test database: %v", err)
	}
	roleCreated := false
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = admin.ExecContext(cleanupCtx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()`, databaseName)
		_, _ = admin.ExecContext(cleanupCtx, `DROP DATABASE IF EXISTS `+databaseIdentifier)
		if roleCreated {
			_, _ = admin.ExecContext(cleanupCtx, `DROP ROLE IF EXISTS `+roleIdentifier)
		}
	})

	source := filepath.Join(t.TempDir(), "costume-tree.sqlite")
	createSQLiteFixture(t, source)
	adminEnv := migrationEnvironment(t, connectionString, config, databaseName, config.User, config.Password)
	output, err := runMigration(t, ctx, source, adminEnv)
	if err != nil {
		t.Fatalf("initial migration failed: %v\n%s", err, output)
	}

	targetConfig := config.Copy()
	targetConfig.Database = databaseName
	target := stdlib.OpenDB(*targetConfig)
	defer func() { _ = target.Close() }()
	var historyCount, productionCount int
	if err := target.QueryRowContext(ctx, `SELECT count(*) FROM public.schema_migrations`).Scan(&historyCount); err != nil {
		t.Fatal(err)
	}
	if err := target.QueryRowContext(ctx, `SELECT count(*) FROM public.productions`).Scan(&productionCount); err != nil {
		t.Fatal(err)
	}
	if historyCount != 2 || productionCount != 1 {
		t.Fatalf("initial migration counts: history=%d productions=%d", historyCount, productionCount)
	}
	if _, err := target.ExecContext(ctx, `INSERT INTO public.productions (name) VALUES ('Post-import production')`); err != nil {
		t.Fatal(err)
	}
	if output, err = runMigration(t, ctx, source, adminEnv); err != nil {
		t.Fatalf("safe rerun failed: %v\n%s", err, output)
	}
	if err := target.QueryRowContext(ctx, `SELECT count(*) FROM public.productions`).Scan(&productionCount); err != nil {
		t.Fatal(err)
	}
	if productionCount != 2 {
		t.Fatalf("rerun production count = %d, want 2", productionCount)
	}

	limitedPassword := "migration-test-" + suffix
	if _, err := admin.ExecContext(ctx, fmt.Sprintf(`CREATE ROLE %s LOGIN PASSWORD '%s'`, roleIdentifier, limitedPassword)); err != nil {
		t.Fatalf("create least-privileged role: %v", err)
	}
	roleCreated = true
	if _, err := admin.ExecContext(ctx, `GRANT CONNECT ON DATABASE `+databaseIdentifier+` TO `+roleIdentifier); err != nil {
		t.Fatal(err)
	}
	if _, err := target.ExecContext(ctx, `GRANT USAGE ON SCHEMA public TO `+roleIdentifier); err != nil {
		t.Fatal(err)
	}
	if _, err := target.ExecContext(ctx, `GRANT SELECT ON ALL TABLES IN SCHEMA public TO `+roleIdentifier); err != nil {
		t.Fatal(err)
	}
	limitedEnv := migrationEnvironment(t, connectionString, config, databaseName, roleName, limitedPassword)
	if output, err = runMigration(t, ctx, source, limitedEnv); err != nil {
		t.Fatalf("least-privileged rerun failed: %v\n%s", err, output)
	}

	differentSource := filepath.Join(t.TempDir(), "different.sqlite")
	content, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(differentSource, content, 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("sqlite3", differentSource, `PRAGMA user_version = 1;`)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("alter SQLite source: %v\n%s", err, output)
	}
	output, err = runMigration(t, ctx, differentSource, adminEnv)
	if err == nil || !strings.Contains(output, "target was loaded from a different SQLite source") {
		t.Fatalf("different source result: err=%v\n%s", err, output)
	}

	if _, err := target.ExecContext(ctx, `DROP TABLE public.sqlite_migration_source`); err != nil {
		t.Fatal(err)
	}
	output, err = runMigration(t, ctx, source, adminEnv)
	if err == nil || !strings.Contains(output, "target contains data without a matching SQLite source marker") {
		t.Fatalf("unmarked target result: err=%v\n%s", err, output)
	}
	if err := target.QueryRowContext(ctx, `SELECT count(*) FROM public.productions`).Scan(&productionCount); err != nil {
		t.Fatal(err)
	}
	if productionCount != 2 {
		t.Fatalf("unmarked target production count = %d, want 2", productionCount)
	}
}

func runMigration(t *testing.T, ctx context.Context, source string, environment []string) (string, error) {
	t.Helper()
	command := exec.CommandContext(ctx, "bash", "migrate-sqlite-to-postgres.sh", source)
	command.Env = environment
	output, err := command.CombinedOutput()
	return string(output), err
}

func migrationEnvironment(t *testing.T, connectionString string, config *pgx.ConnConfig, databaseName, user, password string) []string {
	t.Helper()
	parsed, err := url.Parse(connectionString)
	if err != nil {
		t.Fatalf("parse PG_TEST_URL query: %v", err)
	}
	sslMode := parsed.Query().Get("sslmode")
	if sslMode == "" {
		sslMode = "prefer"
	}
	blocked := map[string]struct{}{
		"PGHOST": {}, "PGPORT": {}, "PGUSER": {}, "PGPASSWORD": {}, "PGDATABASE": {}, "PGSSLMODE": {},
		"PG_HOST": {}, "PG_PORT": {}, "PG_USER": {}, "PG_PASSWORD": {}, "PG_DATABASE": {}, "PG_ADMIN_DB": {}, "PG_SSLMODE": {},
	}
	environment := make([]string, 0, len(os.Environ())+7)
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if _, skip := blocked[key]; !skip {
			environment = append(environment, value)
		}
	}
	return append(environment,
		"PG_HOST="+config.Host,
		"PG_PORT="+strconv.FormatUint(uint64(config.Port), 10),
		"PG_USER="+user,
		"PG_PASSWORD="+password,
		"PG_DATABASE="+databaseName,
		"PG_ADMIN_DB="+config.Database,
		"PG_SSLMODE="+sslMode,
	)
}

func createSQLiteFixture(t *testing.T, path string) {
	t.Helper()
	command := exec.Command("sqlite3", path)
	command.Stdin = strings.NewReader(`
PRAGMA foreign_keys = ON;
CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at TEXT NOT NULL);
CREATE TABLE productions (id INTEGER PRIMARY KEY, name TEXT NOT NULL, archived_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE actors (id INTEGER PRIMARY KEY, production_id INTEGER NOT NULL, name TEXT NOT NULL, role TEXT NOT NULL, notes TEXT NOT NULL, archived_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, FOREIGN KEY (production_id) REFERENCES productions (id));
CREATE TABLE item_types (id INTEGER PRIMARY KEY, production_id INTEGER NOT NULL, name TEXT NOT NULL, archived_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, FOREIGN KEY (production_id) REFERENCES productions (id));
CREATE TABLE production_item_sequences (production_id INTEGER PRIMARY KEY, next_value INTEGER NOT NULL, FOREIGN KEY (production_id) REFERENCES productions (id));
CREATE TABLE costume_items (id INTEGER PRIMARY KEY, production_id INTEGER NOT NULL, actor_id INTEGER NOT NULL, item_type_id INTEGER NOT NULL, code TEXT NOT NULL, description TEXT NOT NULL, status TEXT NOT NULL, progress INTEGER NOT NULL, next_action TEXT NOT NULL, blocker TEXT NOT NULL, notes TEXT NOT NULL, archived_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, FOREIGN KEY (production_id) REFERENCES productions (id), FOREIGN KEY (actor_id) REFERENCES actors (id), FOREIGN KEY (item_type_id) REFERENCES item_types (id));
CREATE TABLE costume_item_photos (id INTEGER PRIMARY KEY, production_id INTEGER NOT NULL, costume_item_id INTEGER NOT NULL, original_name TEXT NOT NULL, display_name TEXT NOT NULL, thumbnail_name TEXT NOT NULL, media_type TEXT NOT NULL, status TEXT NOT NULL, error_message TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, FOREIGN KEY (production_id) REFERENCES productions (id), FOREIGN KEY (costume_item_id) REFERENCES costume_items (id));
INSERT INTO schema_migrations VALUES (1, '0001_initial.sql', '2026-01-01T00:00:00Z'), (2, '0002_costume_item_photos.sql', '2026-01-01T00:00:01Z');
INSERT INTO productions VALUES (1, 'Macbeth', NULL, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z');
INSERT INTO actors VALUES (1, 1, 'Ada', '', '', NULL, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z');
INSERT INTO item_types VALUES (1, 1, 'Cloak', NULL, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z');
INSERT INTO production_item_sequences VALUES (1, 2);
INSERT INTO costume_items VALUES (1, 1, 1, 1, 'C-0001', '', 'Find', 0, '', '', '', NULL, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z');
INSERT INTO costume_item_photos VALUES (1, 1, 1, 'one.jpg', 'one-display.jpg', 'one-thumb.jpg', 'image/jpeg', 'ready', '', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z');
`)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("create SQLite fixture: %v\n%s", err, output)
	}
}
