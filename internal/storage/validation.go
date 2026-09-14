package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

var (
	// ErrInvalidDatabase identifies a PostgreSQL schema that cannot safely be
	// used. The wrapped error includes the failed validation step.
	ErrInvalidDatabase = errors.New("storage: invalid database")
)

var requiredTables = map[string][]string{
	"schema_migrations":         {"version", "name", "applied_at"},
	"productions":               {"id", "name", "archived_at", "created_at", "updated_at"},
	"actors":                    {"id", "production_id", "name", "role", "notes", "archived_at", "created_at", "updated_at"},
	"item_types":                {"id", "production_id", "name", "archived_at", "created_at", "updated_at"},
	"production_item_sequences": {"production_id", "next_value"},
	"costume_items":             {"id", "production_id", "actor_id", "item_type_id", "code", "description", "status", "progress", "next_action", "blocker", "notes", "archived_at", "created_at", "updated_at"},
	"costume_item_photos":       {"id", "production_id", "costume_item_id", "original_name", "display_name", "thumbnail_name", "media_type", "status", "error_message", "created_at", "updated_at"},
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
	"idx_costume_item_photos_item",
	"idx_costume_item_photos_pending",
}

type sqlQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
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
		exists, err := tableExists(ctx, queryer, table)
		if err != nil {
			return fmt.Errorf("%w: inspect required table %q: %v", ErrInvalidDatabase, table, err)
		}
		if !exists {
			return fmt.Errorf("%w: required table %q is missing", ErrInvalidDatabase, table)
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
		var exists bool
		if err := queryer.QueryRowContext(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM pg_catalog.pg_class AS c
				JOIN pg_catalog.pg_namespace AS n ON n.oid = c.relnamespace
				WHERE n.nspname = current_schema()
				  AND c.relname = $1
				  AND c.relkind = 'i'
			)`, index,
		).Scan(&exists); err != nil {
			return fmt.Errorf("%w: inspect required index %q: %v", ErrInvalidDatabase, index, err)
		}
		if !exists {
			return fmt.Errorf("%w: required index %q is missing", ErrInvalidDatabase, index)
		}
	}
	return nil
}

func tableColumns(ctx context.Context, queryer sqlQueryer, table string) (map[string]bool, error) {
	rows, err := queryer.QueryContext(ctx, `
		SELECT column_name
		FROM information_schema.columns
		WHERE table_schema = current_schema()
		  AND table_name = $1`, table)
	if err != nil {
		return nil, fmt.Errorf("%w: inspect table %q: %v", ErrInvalidDatabase, table, err)
	}
	defer rows.Close()
	found := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("%w: inspect table %q: %v", ErrInvalidDatabase, table, err)
		}
		found[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: inspect table %q: %v", ErrInvalidDatabase, table, err)
	}
	return found, nil
}
