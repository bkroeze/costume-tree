// Package storage owns SQLite persistence, schema migrations, and repositories.
package storage

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"sync"

	_ "modernc.org/sqlite"
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
	migrateMu sync.Mutex
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
	// SQLite serializes writers. A bounded pool still permits concurrent reads
	// while the busy timeout handles short writer contention.
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(10)

	result := &DB{db: db}
	if err := result.Ready(context.Background()); err != nil {
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
		"_pragma=busy_timeout(5000)",
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

// Ready verifies that the database is reachable.
func (d *DB) Ready(ctx context.Context) error {
	if d == nil || d.db == nil {
		return errors.New("storage: database is closed")
	}
	if err := d.db.PingContext(ctx); err != nil {
		return fmt.Errorf("storage: ping database: %w", err)
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
func (d *DB) Migrate(ctx context.Context) error {
	if d == nil || d.db == nil {
		return errors.New("storage: database is closed")
	}

	d.migrateMu.Lock()
	defer d.migrateMu.Unlock()

	if _, err := d.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			applied_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
		)`,
	); err != nil {
		return fmt.Errorf("storage: create migration table: %w", err)
	}

	entries, err := fs.ReadDir(migrations, "migrations")
	if err != nil {
		return fmt.Errorf("storage: read embedded migrations: %w", err)
	}
	type migration struct {
		version int
		name    string
	}
	ordered := make([]migration, 0, len(entries))
	seen := make(map[int]struct{}, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		parts := strings.SplitN(entry.Name(), "_", 2)
		if len(parts) != 2 {
			return fmt.Errorf("storage: migration %q has no version prefix", entry.Name())
		}
		version, err := strconv.Atoi(parts[0])
		if err != nil || version <= 0 {
			return fmt.Errorf("storage: migration %q has invalid version", entry.Name())
		}
		if _, exists := seen[version]; exists {
			return fmt.Errorf("storage: duplicate migration version %d", version)
		}
		seen[version] = struct{}{}
		ordered = append(ordered, migration{version: version, name: entry.Name()})
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].version < ordered[j].version })

	applied := make(map[int]struct{}, len(ordered))
	rows, err := d.db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("storage: read applied migrations: %w", err)
	}
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			_ = rows.Close()
			return fmt.Errorf("storage: scan applied migration: %w", err)
		}
		applied[version] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("storage: read applied migrations: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("storage: close migration rows: %w", err)
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
	return nil
}
