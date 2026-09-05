package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

var (
	// ErrInvalidDatabase identifies a database that cannot safely be used or
	// restored. The wrapped error includes the failed validation step.
	ErrInvalidDatabase = errors.New("storage: invalid database")
)

var requiredTables = map[string][]string{
	"schema_migrations":         {"version", "name", "applied_at"},
	"productions":               {"id", "name", "archived_at", "created_at", "updated_at"},
	"actors":                    {"id", "production_id", "name", "role", "notes", "archived_at", "created_at", "updated_at"},
	"item_types":                {"id", "production_id", "name", "archived_at", "created_at", "updated_at"},
	"production_item_sequences": {"production_id", "next_value"},
	"costume_items":             {"id", "production_id", "actor_id", "item_type_id", "code", "description", "status", "progress", "next_action", "blocker", "notes", "archived_at", "created_at", "updated_at"},
}

var requiredIndexes = []string{
	"idx_productions_active",
	"idx_actors_production_active",
	"idx_item_types_active_name",
	"idx_item_types_production_active",
	"idx_costume_items_production_active",
	"idx_costume_items_actor_active",
	"idx_costume_items_type_active",
	"idx_costume_items_status_active",
	"idx_costume_items_code",
}

type sqlQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// Backup writes a transactionally consistent SQLite snapshot to destination.
// The destination must be a new absolute path. VACUUM INTO produces a
// self-contained database and therefore does not require copying a live WAL.
func (d *DB) Backup(ctx context.Context, destination string) (err error) {
	if d == nil || d.db == nil {
		return errors.New("storage: database is closed")
	}
	destination, err = prepareNewPath(destination, "backup destination")
	if err != nil {
		return err
	}
	if d.path != ":memory:" && samePath(d.path, destination) {
		return errors.New("storage: backup destination must differ from source database")
	}
	if err := validateIntegrity(ctx, d.db); err != nil {
		return fmt.Errorf("storage: backup source: %w", err)
	}
	if err := validateSchema(ctx, d.db); err != nil {
		return fmt.Errorf("storage: backup source: %w", err)
	}

	defer func() {
		if err != nil {
			_ = os.Remove(destination)
		}
	}()
	if _, err := d.db.ExecContext(ctx, `VACUUM INTO ?`, destination); err != nil {
		return fmt.Errorf("storage: create backup %q: %w", destination, err)
	}
	if err := os.Chmod(destination, 0o600); err != nil {
		return fmt.Errorf("storage: protect backup %q: %w", destination, err)
	}
	if err := syncFile(destination); err != nil {
		return fmt.Errorf("storage: sync backup %q: %w", destination, err)
	}
	if err := ValidateDatabase(ctx, destination); err != nil {
		return fmt.Errorf("storage: validate backup %q: %w", destination, err)
	}
	return nil
}

// BackupFile opens a migrated database, validates it, and writes a consistent
// snapshot to destination. It is intended for operator commands.
func BackupFile(ctx context.Context, source, destination string) (err error) {
	if _, err := validateExistingDatabasePath(source); err != nil {
		return err
	}
	db, err := Open(source)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := db.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	if err := db.Ready(ctx); err != nil {
		return fmt.Errorf("storage: backup source is not ready: %w", err)
	}
	return db.Backup(ctx, destination)
}

// ValidateDatabase performs cold validation without applying migrations. It
// checks path safety, ownership, SQLite readability/integrity, migration
// history, required tables/columns, and required indexes.
func ValidateDatabase(ctx context.Context, path string) error {
	path, err := validateExistingDatabasePath(path)
	if err != nil {
		return err
	}
	if err := validateOwned(path, "database"); err != nil {
		return err
	}
	ro, err := sql.Open("sqlite", sqliteReadOnlyDSN(path))
	if err != nil {
		return fmt.Errorf("%w: open %q: %v", ErrInvalidDatabase, path, err)
	}
	defer ro.Close()
	ro.SetMaxOpenConns(1)
	ro.SetMaxIdleConns(1)
	if err := ro.PingContext(ctx); err != nil {
		return fmt.Errorf("%w: open %q: %v", ErrInvalidDatabase, path, err)
	}
	if err := validateIntegrity(ctx, ro); err != nil {
		return err
	}
	if err := validateSchema(ctx, ro); err != nil {
		return err
	}
	return nil
}

// Restore copies a validated, self-contained backup to a new database path.
// It intentionally refuses to overwrite an existing path: operators must
// stop the application and choose a fresh destination, avoiding accidental
// replacement of a live database.
func Restore(ctx context.Context, source, destination string) (err error) {
	source, err = validateExistingDatabasePath(source)
	if err != nil {
		return err
	}
	destination, err = prepareNewPath(destination, "restore destination")
	if err != nil {
		return err
	}
	if samePath(source, destination) {
		return errors.New("storage: restore destination must differ from backup")
	}
	if err := ValidateDatabase(ctx, source); err != nil {
		return fmt.Errorf("storage: restore source: %w", err)
	}

	input, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("storage: open restore source %q: %w", source, err)
	}
	defer input.Close()
	tmp, err := os.CreateTemp(filepath.Dir(destination), "."+filepath.Base(destination)+".restore-*")
	if err != nil {
		return fmt.Errorf("storage: create restore temporary file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = os.Remove(tmpName)
	}()
	if err := copyWithContext(ctx, tmp, input); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("storage: copy restore source: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("storage: protect restored database: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("storage: sync restored database: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("storage: close restored database: %w", err)
	}
	if err := os.Rename(tmpName, destination); err != nil {
		return fmt.Errorf("storage: install restored database %q: %w", destination, err)
	}
	if err := syncDirectory(filepath.Dir(destination)); err != nil {
		return fmt.Errorf("storage: sync restore directory: %w", err)
	}
	if err := ValidateDatabase(ctx, destination); err != nil {
		_ = os.Remove(destination)
		return fmt.Errorf("storage: validate restored database %q: %w", destination, err)
	}
	return nil
}

func sqliteReadOnlyDSN(path string) string {
	return "file:" + path + "?mode=ro&_pragma=foreign_keys(1)&_pragma=busy_timeout(" +
		fmt.Sprint(sqliteBusyTimeout.Milliseconds()) + ")"
}

func validateIntegrity(ctx context.Context, queryer sqlQueryer) error {
	var result string
	if err := queryer.QueryRowContext(ctx, `PRAGMA integrity_check(1)`).Scan(&result); err != nil {
		return fmt.Errorf("%w: sqlite integrity check: %v", ErrInvalidDatabase, err)
	}
	if result != "ok" {
		return fmt.Errorf("%w: sqlite integrity check returned %q", ErrInvalidDatabase, result)
	}

	rows, err := queryer.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return fmt.Errorf("%w: sqlite foreign-key check: %v", ErrInvalidDatabase, err)
	}
	defer rows.Close()
	if rows.Next() {
		var table, parent string
		var rowID, foreignKey int64
		if err := rows.Scan(&table, &rowID, &parent, &foreignKey); err != nil {
			return fmt.Errorf("%w: scan foreign-key check: %v", ErrInvalidDatabase, err)
		}
		return fmt.Errorf("%w: foreign-key violation in %s row %d (parent %s, constraint %d)", ErrInvalidDatabase, table, rowID, parent, foreignKey)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("%w: sqlite foreign-key check: %v", ErrInvalidDatabase, err)
	}
	return nil
}

func validateSchema(ctx context.Context, queryer sqlQueryer) error {
	ordered, err := embeddedMigrations()
	if err != nil {
		return err
	}
	applied, err := readMigrationHistory(ctx, queryer)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidDatabase, err)
	}
	if err := validateMigrationHistory(applied, ordered); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidDatabase, err)
	}
	if len(applied) != len(ordered) {
		return fmt.Errorf("%w: migration history has %d applied migration(s), want %d", ErrInvalidDatabase, len(applied), len(ordered))
	}

	for table, columns := range requiredTables {
		var kind string
		if err := queryer.QueryRowContext(ctx,
			`SELECT type FROM sqlite_master WHERE name = ?`, table,
		).Scan(&kind); err != nil {
			return fmt.Errorf("%w: required table %q is missing: %v", ErrInvalidDatabase, table, err)
		}
		if kind != "table" {
			return fmt.Errorf("%w: required object %q is %s, want table", ErrInvalidDatabase, table, kind)
		}
		found, err := tableColumns(ctx, queryer, table)
		if err != nil {
			return err
		}
		for _, column := range columns {
			if !found[column] {
				return fmt.Errorf("%w: table %q is missing column %q", ErrInvalidDatabase, table, column)
			}
		}
	}
	for _, index := range requiredIndexes {
		var kind string
		if err := queryer.QueryRowContext(ctx,
			`SELECT type FROM sqlite_master WHERE name = ?`, index,
		).Scan(&kind); err != nil {
			return fmt.Errorf("%w: required index %q is missing: %v", ErrInvalidDatabase, index, err)
		}
		if kind != "index" {
			return fmt.Errorf("%w: required object %q is %s, want index", ErrInvalidDatabase, index, kind)
		}
	}
	return nil
}

func tableColumns(ctx context.Context, queryer sqlQueryer, table string) (map[string]bool, error) {
	rows, err := queryer.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return nil, fmt.Errorf("%w: inspect table %q: %v", ErrInvalidDatabase, table, err)
	}
	defer rows.Close()
	found := make(map[string]bool)
	for rows.Next() {
		var cid int
		var name, dataType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, fmt.Errorf("%w: inspect table %q: %v", ErrInvalidDatabase, table, err)
		}
		found[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: inspect table %q: %v", ErrInvalidDatabase, table, err)
	}
	return found, nil
}

func prepareNewPath(path, label string) (string, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) == string(filepath.Separator) || path == ":memory:" || filepath.VolumeName(path) != "" {
		return "", fmt.Errorf("storage: %s must be a new absolute filesystem path", label)
	}
	clean := filepath.Clean(path)
	if _, err := os.Lstat(clean); err == nil {
		return "", fmt.Errorf("storage: %s %q already exists; use a fresh path", label, clean)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("storage: inspect %s %q: %w", label, clean, err)
	}
	parent := filepath.Dir(clean)
	info, err := os.Lstat(parent)
	if err != nil {
		return "", fmt.Errorf("storage: inspect %s directory %q: %w", label, parent, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("storage: %s directory %q is not a directory", label, parent)
	}
	if err := validateOwned(parent, label+" directory"); err != nil {
		return "", err
	}
	return clean, nil
}

func validateExistingDatabasePath(path string) (string, error) {
	if path == "" || path == ":memory:" || !filepath.IsAbs(path) || filepath.VolumeName(path) != "" {
		return "", fmt.Errorf("storage: database path must be an absolute filesystem path")
	}
	clean := filepath.Clean(path)
	info, err := os.Lstat(clean)
	if err != nil {
		return "", fmt.Errorf("storage: inspect database %q: %w", clean, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("storage: database %q must be a regular file", clean)
	}
	return clean, nil
}

func validateOwned(path, label string) error {
	if os.Geteuid() == 0 {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("storage: inspect %s ownership %q: %w", label, path, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || uint64(stat.Uid) != uint64(os.Getuid()) {
		return fmt.Errorf("storage: %s %q is not owned by uid %d", label, path, os.Getuid())
	}
	return nil
}

func samePath(first, second string) bool {
	first = filepath.Clean(first)
	second = filepath.Clean(second)
	if abs, err := filepath.Abs(first); err == nil {
		first = abs
	}
	if abs, err := filepath.Abs(second); err == nil {
		second = abs
	}
	return first == second
}

func syncFile(path string) error {
	file, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func copyWithContext(ctx context.Context, destination io.Writer, source io.Reader) error {
	buffer := make([]byte, 128*1024)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		count, readErr := source.Read(buffer)
		if count > 0 {
			written := 0
			for written < count {
				if err := ctx.Err(); err != nil {
					return err
				}
				n, err := destination.Write(buffer[written:count])
				written += n
				if err != nil {
					return err
				}
				if n == 0 {
					return io.ErrShortWrite
				}
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			return readErr
		}
	}
}
