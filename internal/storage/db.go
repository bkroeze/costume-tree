// Package storage owns SQLite persistence, schema migrations, and repositories.
package storage

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	_ "modernc.org/sqlite"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// migrations contains the ordered schema migrations shipped with the binary.
// Migration filenames must begin with a zero-padded decimal version followed by
// an underscore (for example, 0001_initial.sql).
//
//go:embed migrations/*.sql
var migrations embed.FS

// DB is an opened SQLite database. Call Migrate before using repositories.
type DB struct {
	db        *sql.DB
	path      string
	migrateMu sync.Mutex
}

const (
	sqliteMaxOpenConns = 4
	sqliteMaxIdleConns = 4
	sqliteBusyTimeout  = 5 * time.Second
)

type migration struct {
	version int
	name    string
}

// Open opens path with SQLite settings suitable for the application. A path of
// :memory: is supported for tests; ordinary paths are created when absent.
func Open(path string) (*DB, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("storage: database path is required")
	}

	db, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		return nil, fmt.Errorf("storage: open database: %w", err)
	}
	// SQLite has one writer. A small fixed pool allows a few readers without
	// creating an unbounded queue of connections, while every connection gets
	// the same busy timeout through sqliteDSN.
	db.SetMaxOpenConns(sqliteMaxOpenConns)
	db.SetMaxIdleConns(sqliteMaxIdleConns)

	result := &DB{db: db, path: path}
	if err := db.PingContext(context.Background()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("storage: open database: %w: ping: %v", ErrInvalidDatabase, err)
	}
	if err := validateIntegrity(context.Background(), db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("storage: open database: %w", err)
	}
	return result, nil
}

func sqliteDSN(path string) string {
	if path == ":memory:" {
		path = "file::memory:?cache=shared"
	} else if !strings.HasPrefix(path, "file:") {
		path = "file:" + path
	}

	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	return path + separator + strings.Join([]string{
		"_pragma=foreign_keys(1)",
		"_pragma=busy_timeout(" + strconv.FormatInt(sqliteBusyTimeout.Milliseconds(), 10) + ")",
		"_pragma=journal_mode(WAL)",
		"_pragma=synchronous(NORMAL)",
		"_txlock=immediate",
	}, "&")
}

// SQL returns the underlying database handle for package-internal composition
// and narrowly-scoped queries. Callers should prefer repository contracts.
func (d *DB) SQL() *sql.DB {
	if d == nil {
		return nil
	}
	return d.db
}

// Ready verifies that the database is reachable, internally consistent, and
// has the complete schema recorded by the embedded migrations. Migrate must be
// called first on a fresh database.
func (d *DB) Ready(ctx context.Context) error {
	if d == nil || d.db == nil {
		return errors.New("storage: database is closed")
	}
	if err := d.db.PingContext(ctx); err != nil {
		return fmt.Errorf("storage: ping database: %w", err)
	}
	if err := validateIntegrity(ctx, d.db); err != nil {
		return err
	}
	if err := validateSchema(ctx, d.db); err != nil {
		return err
	}
	return nil
}

// Close releases all database connections.
func (d *DB) Close() error {
	if d == nil || d.db == nil {
		return nil
	}
	if err := d.db.Close(); err != nil {
		return fmt.Errorf("storage: close database: %w", err)
	}
	return nil
}

// Migrate applies each embedded migration exactly once, in version order.
// Existing databases without migration history are never modified unless
// they are genuinely empty; this prevents a damaged or foreign database from
// being silently rebuilt over.
func (d *DB) Migrate(ctx context.Context) error {
	if d == nil || d.db == nil {
		return errors.New("storage: database is closed")
	}

	d.migrateMu.Lock()
	defer d.migrateMu.Unlock()

	if err := validateIntegrity(ctx, d.db); err != nil {
		return err
	}
	ordered, err := embeddedMigrations()
	if err != nil {
		return err
	}
	if len(ordered) == 0 {
		return errors.New("storage: no embedded migrations")
	}

	hasHistory, err := tableExists(ctx, d.db, "schema_migrations")
	if err != nil {
		return fmt.Errorf("storage: inspect migration history: %w", err)
	}
	if !hasHistory {
		userTables, err := userTableCount(ctx, d.db)
		if err != nil {
			return fmt.Errorf("storage: inspect existing schema: %w", err)
		}
		if userTables != 0 {
			return fmt.Errorf("storage: database has %d table(s) but no migration history; refusing to reapply migrations", userTables)
		}
		if _, err := d.db.ExecContext(ctx, `
			CREATE TABLE schema_migrations (
				version INTEGER PRIMARY KEY,
				name TEXT NOT NULL,
				applied_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
			)`,
		); err != nil {
			return fmt.Errorf("storage: create migration table: %w", err)
		}
	}

	applied, err := readMigrationHistory(ctx, d.db)
	if err != nil {
		return err
	}
	if err := validateMigrationHistory(applied, ordered); err != nil {
		return err
	}

	for _, migration := range ordered {
		if _, ok := applied[migration.version]; ok {
			continue
		}
		script, err := fs.ReadFile(migrations, "migrations/"+migration.name)
		if err != nil {
			return fmt.Errorf("storage: read migration %q: %w", migration.name, err)
		}
		tx, err := d.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("storage: begin migration %q: %w", migration.name, err)
		}
		if _, err := tx.ExecContext(ctx, string(script)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("storage: apply migration %q: %w", migration.name, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations (version, name) VALUES (?, ?)`, migration.version, migration.name,
		); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("storage: record migration %q: %w", migration.name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("storage: commit migration %q: %w", migration.name, err)
		}
	}
	if err := validateSchema(ctx, d.db); err != nil {
		return fmt.Errorf("storage: migration validation: %w", err)
	}
	return nil
}

func embeddedMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrations, "migrations")
	if err != nil {
		return nil, fmt.Errorf("storage: read embedded migrations: %w", err)
	}
	ordered := make([]migration, 0, len(entries))
	seen := make(map[int]struct{}, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		parts := strings.SplitN(entry.Name(), "_", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("storage: migration %q has no version prefix", entry.Name())
		}
		version, err := strconv.Atoi(parts[0])
		if err != nil || version <= 0 {
			return nil, fmt.Errorf("storage: migration %q has invalid version", entry.Name())
		}
		if _, exists := seen[version]; exists {
			return nil, fmt.Errorf("storage: duplicate migration version %d", version)
		}
		seen[version] = struct{}{}
		ordered = append(ordered, migration{version: version, name: entry.Name()})
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].version < ordered[j].version })
	return ordered, nil
}

func tableExists(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, name string) (bool, error) {
	var count int
	err := queryer.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name,
	).Scan(&count)
	return count != 0, err
}

func userTableCount(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (int, error) {
	var count int
	err := queryer.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`,
	).Scan(&count)
	return count, err
}

func readMigrationHistory(ctx context.Context, queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}) (map[int]string, error) {
	rows, err := queryer.QueryContext(ctx, `SELECT version, name FROM schema_migrations ORDER BY version`)
	if err != nil {
		return nil, fmt.Errorf("storage: read applied migrations: %w", err)
	}
	defer rows.Close()
	applied := make(map[int]string)
	for rows.Next() {
		var version int
		var name string
		if err := rows.Scan(&version, &name); err != nil {
			return nil, fmt.Errorf("storage: scan applied migration: %w", err)
		}
		applied[version] = name
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: read applied migrations: %w", err)
	}
	return applied, nil
}

func validateMigrationHistory(applied map[int]string, ordered []migration) error {
	known := make(map[int]string, len(ordered))
	for _, migration := range ordered {
		known[migration.version] = migration.name
	}
	for version, name := range applied {
		expected, ok := known[version]
		if !ok {
			return fmt.Errorf("storage: database records unknown migration version %d; refusing to reapply migrations", version)
		}
		if name != expected {
			return fmt.Errorf("storage: migration %d is recorded as %q, want %q", version, name, expected)
		}
	}
	return nil
}
