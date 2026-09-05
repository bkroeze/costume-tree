package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestBackupRestorePreservesRepresentativeRows(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.db")
	backupPath := filepath.Join(dir, "backups", "source.snapshot.db")
	restoredPath := filepath.Join(dir, "restored.db")
	if err := os.Mkdir(filepath.Dir(backupPath), 0o755); err != nil {
		t.Fatal(err)
	}

	db, err := Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx); err != nil {
		db.Close()
		t.Fatal(err)
	}
	production, err := NewProductionRepository(db).Create(ctx, CreateProductionInput{Name: "Macbeth"})
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Backup(ctx, backupPath); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	if err := ValidateDatabase(ctx, backupPath); err != nil {
		t.Fatalf("ValidateDatabase(backup) error = %v", err)
	}
	if err := Restore(ctx, backupPath, restoredPath); err != nil {
		t.Fatal(err)
	}

	restored, err := Open(restoredPath)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if err := restored.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := NewProductionRepository(restored).List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != production.ID || rows[0].Name != "Macbeth" {
		t.Fatalf("restored productions = %#v", rows)
	}
}

func TestCorruptDatabaseFailsOpenAndReadiness(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "corrupt.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := NewProductionRepository(db).Create(ctx, CreateProductionInput{Name: "Macbeth"}); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not a sqlite database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("Open(corrupt database) returned nil error")
	} else if !errors.Is(err, ErrInvalidDatabase) {
		t.Fatalf("Open(corrupt database) error = %v, want ErrInvalidDatabase", err)
	}
	if err := ValidateDatabase(ctx, path); err == nil {
		t.Fatal("ValidateDatabase(corrupt database) returned nil error")
	} else if !errors.Is(err, ErrInvalidDatabase) {
		t.Fatalf("ValidateDatabase(corrupt database) error = %v, want ErrInvalidDatabase", err)
	}
}

func TestReadinessRejectsMissingSchemaObjectWithoutReapply(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "missing-table.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.SQL().ExecContext(ctx, `DROP TABLE actors`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Ready(ctx); err == nil {
		db.Close()
		t.Fatal("Ready() returned nil for missing required table")
	} else if !errors.Is(err, ErrInvalidDatabase) {
		db.Close()
		t.Fatalf("Ready() error = %v, want ErrInvalidDatabase", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.Migrate(ctx); err == nil {
		t.Fatal("Migrate() repaired missing table; expected non-destructive failure")
	} else if !errors.Is(err, ErrInvalidDatabase) {
		t.Fatalf("Migrate() error = %v, want ErrInvalidDatabase", err)
	}
}

func TestMigrateRefusesUntrackedSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "untracked.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.SQL().ExecContext(ctx, `CREATE TABLE foreign_data (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx); err == nil {
		t.Fatal("Migrate() applied schema to untracked database")
	}
	var count int
	if err := db.SQL().QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE name = 'schema_migrations'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("Migrate() created migration history on untracked database")
	}
}

func TestOpenUsesBoundedSQLitePool(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "pool.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	stats := db.SQL().Stats()
	if stats.MaxOpenConnections != sqliteMaxOpenConns {
		t.Fatalf("MaxOpenConnections = %d, want %d", stats.MaxOpenConnections, sqliteMaxOpenConns)
	}
	if sqliteBusyTimeout <= 0 {
		t.Fatal("busy timeout must be positive")
	}
}
