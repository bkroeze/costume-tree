package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type rowQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (d *DB) begin(ctx context.Context) (*sql.Tx, error) {
	if d == nil || d.db == nil {
		return nil, errors.New("storage: database is closed")
	}
	return d.db.BeginTx(ctx, nil)
}

func notFound(kind string) error {
	return fmt.Errorf("%w: %s", ErrNotFound, kind)
}

func parseTimestamp(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("storage: parse timestamp %q: %w", value, err)
	}
	return parsed, nil
}

func scanProduction(scanner interface{ Scan(...any) error }) (Production, error) {
	var result Production
	var archived, created, updated string
	if err := scanner.Scan(&result.ID, &result.Name, &archived, &created, &updated); err != nil {
		return Production{}, err
	}
	var err error
	if result.ArchivedAt, err = archivedTimestamp(archived); err != nil {
		return Production{}, err
	}
	if result.CreatedAt, err = parseTimestamp(created); err != nil {
		return Production{}, err
	}
	if result.UpdatedAt, err = parseTimestamp(updated); err != nil {
		return Production{}, err
	}
	return result, nil
}

func archivedTimestamp(value string) (*time.Time, error) {
	if value == "" {
		return nil, nil
	}
	parsed, err := parseTimestamp(value)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func scanActor(scanner interface{ Scan(...any) error }) (Actor, error) {
	var result Actor
	var archived, created, updated string
	if err := scanner.Scan(&result.ID, &result.ProductionID, &result.Name, &result.Role, &result.Notes, &archived, &created, &updated); err != nil {
		return Actor{}, err
	}
	var err error
	if result.ArchivedAt, err = archivedTimestamp(archived); err != nil {
		return Actor{}, err
	}
	if result.CreatedAt, err = parseTimestamp(created); err != nil {
		return Actor{}, err
	}
	if result.UpdatedAt, err = parseTimestamp(updated); err != nil {
		return Actor{}, err
	}
	return result, nil
}

func scanItemType(scanner interface{ Scan(...any) error }) (ItemType, error) {
	var result ItemType
	var archived, created, updated string
	if err := scanner.Scan(&result.ID, &result.ProductionID, &result.Name, &archived, &created, &updated); err != nil {
		return ItemType{}, err
	}
	var err error
	if result.ArchivedAt, err = archivedTimestamp(archived); err != nil {
		return ItemType{}, err
	}
	if result.CreatedAt, err = parseTimestamp(created); err != nil {
		return ItemType{}, err
	}
	if result.UpdatedAt, err = parseTimestamp(updated); err != nil {
		return ItemType{}, err
	}
	return result, nil
}

func scanCostumeItem(scanner interface{ Scan(...any) error }) (CostumeItem, error) {
	var result CostumeItem
	var archived, created, updated string
	if err := scanner.Scan(
		&result.ID, &result.ProductionID, &result.ActorID, &result.ItemTypeID,
		&result.Code, &result.Description, &result.Status, &result.Progress,
		&result.NextAction, &result.Blocker, &result.Notes,
		&archived, &created, &updated,
	); err != nil {
		return CostumeItem{}, err
	}
	var err error
	if result.ArchivedAt, err = archivedTimestamp(archived); err != nil {
		return CostumeItem{}, err
	}
	if result.CreatedAt, err = parseTimestamp(created); err != nil {
		return CostumeItem{}, err
	}
	if result.UpdatedAt, err = parseTimestamp(updated); err != nil {
		return CostumeItem{}, err
	}
	return result, nil
}

func ensureProduction(ctx context.Context, query rowQuerier, id int64) error {
	var archived string
	if err := query.QueryRowContext(ctx,
		`SELECT COALESCE(archived_at, '') FROM productions WHERE id = ?`, id,
	).Scan(&archived); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return notFound("production")
		}
		return err
	}
	if archived != "" {
		return ErrArchived
	}
	return nil
}

func ensureActor(ctx context.Context, query rowQuerier, productionID, actorID int64) error {
	var archived string
	if err := query.QueryRowContext(ctx,
		`SELECT COALESCE(archived_at, '') FROM actors WHERE production_id = ? AND id = ?`, productionID, actorID,
	).Scan(&archived); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return notFound("actor")
		}
		return err
	}
	if archived != "" {
		return ErrArchived
	}
	return nil
}

func ensureItemType(ctx context.Context, query rowQuerier, productionID, itemTypeID int64) error {
	var archived string
	if err := query.QueryRowContext(ctx,
		`SELECT COALESCE(archived_at, '') FROM item_types WHERE production_id = ? AND id = ?`, productionID, itemTypeID,
	).Scan(&archived); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return notFound("item type")
		}
		return err
	}
	if archived != "" {
		return ErrArchived
	}
	return nil
}

// Production repositories.
type productionRepository struct{ db *DB }

func NewProductionRepository(db *DB) ProductionRepository {
	return &productionRepository{db: db}
}

const productionColumns = `id, name, COALESCE(archived_at, ''), created_at, updated_at`

func (r *productionRepository) Create(ctx context.Context, input CreateProductionInput) (Production, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return Production{}, errors.New("storage: production name is required")
	}
	if r.db == nil || r.db.db == nil {
		return Production{}, errors.New("storage: database is closed")
	}
	result, err := r.db.db.ExecContext(ctx, `INSERT INTO productions (name) VALUES (?)`, name)
	if err != nil {
		return Production{}, fmt.Errorf("storage: create production: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return Production{}, fmt.Errorf("storage: create production id: %w", err)
	}
	return r.Get(ctx, id)
}

func (r *productionRepository) Get(ctx context.Context, id int64) (Production, error) {
	if r.db == nil || r.db.db == nil {
		return Production{}, errors.New("storage: database is closed")
	}
	result, err := scanProduction(r.db.db.QueryRowContext(ctx,
		`SELECT `+productionColumns+` FROM productions WHERE id = ?`, id,
	))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Production{}, notFound("production")
		}
		return Production{}, fmt.Errorf("storage: get production: %w", err)
	}
	return result, nil
}

func (r *productionRepository) List(ctx context.Context, includeArchived ...bool) ([]Production, error) {
	if r.db == nil || r.db.db == nil {
		return nil, errors.New("storage: database is closed")
	}
	query := `SELECT ` + productionColumns + ` FROM productions`
	if !firstBool(includeArchived) {
		query += ` WHERE archived_at IS NULL`
	}
	query += ` ORDER BY name, id`
	rows, err := r.db.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("storage: list productions: %w", err)
	}
	defer rows.Close()
	result := make([]Production, 0)
	for rows.Next() {
		item, err := scanProduction(rows)
		if err != nil {
			return nil, fmt.Errorf("storage: scan production: %w", err)
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list productions: %w", err)
	}
	return result, nil
}

func (r *productionRepository) Update(ctx context.Context, input UpdateProductionInput) (Production, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return Production{}, errors.New("storage: production name is required")
	}
	if r.db == nil || r.db.db == nil {
		return Production{}, errors.New("storage: database is closed")
	}
	result, err := r.db.db.ExecContext(ctx,
		`UPDATE productions SET name = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ?`, name, input.ID,
	)
	if err != nil {
		return Production{}, fmt.Errorf("storage: update production: %w", err)
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return Production{}, notFound("production")
	}
	return r.Get(ctx, input.ID)
}

func (r *productionRepository) Archive(ctx context.Context, id int64) error {
	if r.db == nil || r.db.db == nil {
		return errors.New("storage: database is closed")
	}
	result, err := r.db.db.ExecContext(ctx,
		`UPDATE productions SET archived_at = COALESCE(archived_at, strftime('%Y-%m-%dT%H:%M:%fZ', 'now')), updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ?`, id,
	)
	if err != nil {
		return fmt.Errorf("storage: archive production: %w", err)
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return notFound("production")
	}
	return nil
}

func firstBool(values []bool) bool {
	return len(values) > 0 && values[0]
}

// Actor repositories.
type actorRepository struct{ db *DB }

func NewActorRepository(db *DB) ActorRepository { return &actorRepository{db: db} }

const actorColumns = `id, production_id, name, role, notes, COALESCE(archived_at, ''), created_at, updated_at`

func (r *actorRepository) Create(ctx context.Context, input CreateActorInput) (Actor, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return Actor{}, errors.New("storage: actor name is required")
	}
	tx, err := r.db.begin(ctx)
	if err != nil {
		return Actor{}, err
	}
	defer tx.Rollback()
	if err := ensureProduction(ctx, tx, input.ProductionID); err != nil {
		return Actor{}, err
	}
	result, err := tx.ExecContext(ctx,
		`INSERT INTO actors (production_id, name, role, notes) VALUES (?, ?, ?, ?)`,
		input.ProductionID, name, input.Role, input.Notes,
	)
	if err != nil {
		return Actor{}, fmt.Errorf("storage: create actor: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return Actor{}, fmt.Errorf("storage: create actor id: %w", err)
	}
	item, err := scanActor(tx.QueryRowContext(ctx, `SELECT `+actorColumns+` FROM actors WHERE production_id = ? AND id = ?`, input.ProductionID, id))
	if err != nil {
		return Actor{}, fmt.Errorf("storage: read actor: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Actor{}, fmt.Errorf("storage: commit actor: %w", err)
	}
	return item, nil
}

func (r *actorRepository) Get(ctx context.Context, productionID, id int64) (Actor, error) {
	item, err := scanActor(r.db.db.QueryRowContext(ctx,
		`SELECT `+actorColumns+` FROM actors WHERE production_id = ? AND id = ?`, productionID, id,
	))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Actor{}, notFound("actor")
		}
		return Actor{}, fmt.Errorf("storage: get actor: %w", err)
	}
	return item, nil
}

func (r *actorRepository) List(ctx context.Context, productionID int64, includeArchived ...bool) ([]Actor, error) {
	query := `SELECT ` + actorColumns + ` FROM actors WHERE production_id = ?`
	args := []any{productionID}
	if !firstBool(includeArchived) {
		query += ` AND archived_at IS NULL`
	}
	query += ` ORDER BY name, id`
	rows, err := r.db.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("storage: list actors: %w", err)
	}
	defer rows.Close()
	result := make([]Actor, 0)
	for rows.Next() {
		item, err := scanActor(rows)
		if err != nil {
			return nil, fmt.Errorf("storage: scan actor: %w", err)
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list actors: %w", err)
	}
	return result, nil
}

func (r *actorRepository) Update(ctx context.Context, input UpdateActorInput) (Actor, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return Actor{}, errors.New("storage: actor name is required")
	}
	result, err := r.db.db.ExecContext(ctx,
		`UPDATE actors SET name = ?, role = ?, notes = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE production_id = ? AND id = ?`,
		name, input.Role, input.Notes, input.ProductionID, input.ID,
	)
	if err != nil {
		return Actor{}, fmt.Errorf("storage: update actor: %w", err)
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return Actor{}, notFound("actor")
	}
	return r.Get(ctx, input.ProductionID, input.ID)
}

func (r *actorRepository) Archive(ctx context.Context, productionID, id int64) error {
	result, err := r.db.db.ExecContext(ctx,
		`UPDATE actors SET archived_at = COALESCE(archived_at, strftime('%Y-%m-%dT%H:%M:%fZ', 'now')), updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE production_id = ? AND id = ?`,
		productionID, id,
	)
	if err != nil {
		return fmt.Errorf("storage: archive actor: %w", err)
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return notFound("actor")
	}
	return nil
}

// Item-type repositories.
type itemTypeRepository struct{ db *DB }

func NewItemTypeRepository(db *DB) ItemTypeRepository { return &itemTypeRepository{db: db} }

const itemTypeColumns = `id, production_id, name, COALESCE(archived_at, ''), created_at, updated_at`

func (r *itemTypeRepository) Create(ctx context.Context, input CreateItemTypeInput) (ItemType, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return ItemType{}, errors.New("storage: item type name is required")
	}
	tx, err := r.db.begin(ctx)
	if err != nil {
		return ItemType{}, err
	}
	defer tx.Rollback()
	if err := ensureProduction(ctx, tx, input.ProductionID); err != nil {
		return ItemType{}, err
	}
	result, err := tx.ExecContext(ctx,
		`INSERT INTO item_types (production_id, name) VALUES (?, ?)`, input.ProductionID, name,
	)
	if err != nil {
		return ItemType{}, fmt.Errorf("storage: create item type: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return ItemType{}, fmt.Errorf("storage: create item type id: %w", err)
	}
	item, err := scanItemType(tx.QueryRowContext(ctx, `SELECT `+itemTypeColumns+` FROM item_types WHERE production_id = ? AND id = ?`, input.ProductionID, id))
	if err != nil {
		return ItemType{}, fmt.Errorf("storage: read item type: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return ItemType{}, fmt.Errorf("storage: commit item type: %w", err)
	}
	return item, nil
}

func (r *itemTypeRepository) Get(ctx context.Context, productionID, id int64) (ItemType, error) {
	item, err := scanItemType(r.db.db.QueryRowContext(ctx,
		`SELECT `+itemTypeColumns+` FROM item_types WHERE production_id = ? AND id = ?`, productionID, id,
	))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ItemType{}, notFound("item type")
		}
		return ItemType{}, fmt.Errorf("storage: get item type: %w", err)
	}
	return item, nil
}

func (r *itemTypeRepository) List(ctx context.Context, productionID int64, includeArchived ...bool) ([]ItemType, error) {
	query := `SELECT ` + itemTypeColumns + ` FROM item_types WHERE production_id = ?`
	if !firstBool(includeArchived) {
		query += ` AND archived_at IS NULL`
	}
	query += ` ORDER BY name, id`
	rows, err := r.db.db.QueryContext(ctx, query, productionID)
	if err != nil {
		return nil, fmt.Errorf("storage: list item types: %w", err)
	}
	defer rows.Close()
	result := make([]ItemType, 0)
	for rows.Next() {
		item, err := scanItemType(rows)
		if err != nil {
			return nil, fmt.Errorf("storage: scan item type: %w", err)
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list item types: %w", err)
	}
	return result, nil
}

func (r *itemTypeRepository) Update(ctx context.Context, input UpdateItemTypeInput) (ItemType, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return ItemType{}, errors.New("storage: item type name is required")
	}
	result, err := r.db.db.ExecContext(ctx,
		`UPDATE item_types SET name = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE production_id = ? AND id = ?`,
		name, input.ProductionID, input.ID,
	)
	if err != nil {
		return ItemType{}, fmt.Errorf("storage: update item type: %w", err)
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return ItemType{}, notFound("item type")
	}
	return r.Get(ctx, input.ProductionID, input.ID)
}

func (r *itemTypeRepository) Archive(ctx context.Context, productionID, id int64) error {
	result, err := r.db.db.ExecContext(ctx,
		`UPDATE item_types SET archived_at = COALESCE(archived_at, strftime('%Y-%m-%dT%H:%M:%fZ', 'now')), updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE production_id = ? AND id = ?`,
		productionID, id,
	)
	if err != nil {
		return fmt.Errorf("storage: archive item type: %w", err)
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return notFound("item type")
	}
	return nil
}

// Restore reactivates an archived item type while preserving its ID.
func (r *itemTypeRepository) Restore(ctx context.Context, productionID, id int64) error {
	result, err := r.db.db.ExecContext(ctx,
		`UPDATE item_types SET archived_at = NULL, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE production_id = ? AND id = ?`,
		productionID, id,
	)
	if err != nil {
		return fmt.Errorf("storage: restore item type: %w", err)
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return notFound("item type")
	}
	return nil
}

// Costume-item repositories.
type costumeItemRepository struct{ db *DB }

func NewCostumeItemRepository(db *DB) CostumeItemRepository {
	return &costumeItemRepository{db: db}
}

const costumeItemColumns = `id, production_id, actor_id, item_type_id, code, description, status, progress, next_action, blocker, notes, COALESCE(archived_at, ''), created_at, updated_at`

func allocateCodeTx(ctx context.Context, tx *sql.Tx, productionID int64) (string, error) {
	if err := ensureProduction(ctx, tx, productionID); err != nil {
		return "", err
	}
	var value int64
	if err := tx.QueryRowContext(ctx,
		`INSERT INTO production_item_sequences (production_id, next_value) VALUES (?, 1)
		 ON CONFLICT (production_id) DO UPDATE SET next_value = production_item_sequences.next_value + 1
		 RETURNING next_value`,
		productionID,
	).Scan(&value); err != nil {
		return "", fmt.Errorf("storage: allocate item code: %w", err)
	}
	return fmt.Sprintf("C-%04d", value), nil
}

func (r *costumeItemRepository) AllocateCode(ctx context.Context, productionID int64) (string, error) {
	tx, err := r.db.begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	code, err := allocateCodeTx(ctx, tx, productionID)
	if err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("storage: commit item code allocation: %w", err)
	}
	return code, nil
}

func (r *costumeItemRepository) Create(ctx context.Context, input CreateCostumeItemInput) (CostumeItem, error) {
	status := input.Status
	if status == "" {
		status = StatusFind
	}
	tx, err := r.db.begin(ctx)
	if err != nil {
		return CostumeItem{}, err
	}
	defer tx.Rollback()
	if err := ensureProduction(ctx, tx, input.ProductionID); err != nil {
		return CostumeItem{}, err
	}
	if err := ensureActor(ctx, tx, input.ProductionID, input.ActorID); err != nil {
		return CostumeItem{}, err
	}
	if err := ensureItemType(ctx, tx, input.ProductionID, input.ItemTypeID); err != nil {
		return CostumeItem{}, err
	}
	code, err := allocateCodeTx(ctx, tx, input.ProductionID)
	if err != nil {
		return CostumeItem{}, err
	}
	result, err := tx.ExecContext(ctx,
		`INSERT INTO costume_items (production_id, actor_id, item_type_id, code, description, status, progress, next_action, blocker, notes)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		input.ProductionID, input.ActorID, input.ItemTypeID, code, input.Description, status, input.Progress,
		input.NextAction, input.Blocker, input.Notes,
	)
	if err != nil {
		return CostumeItem{}, fmt.Errorf("storage: create costume item: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return CostumeItem{}, fmt.Errorf("storage: create costume item id: %w", err)
	}
	item, err := scanCostumeItem(tx.QueryRowContext(ctx,
		`SELECT `+costumeItemColumns+` FROM costume_items WHERE production_id = ? AND id = ?`, input.ProductionID, id,
	))
	if err != nil {
		return CostumeItem{}, fmt.Errorf("storage: read costume item: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return CostumeItem{}, fmt.Errorf("storage: commit costume item: %w", err)
	}
	return item, nil
}

func (r *costumeItemRepository) Get(ctx context.Context, productionID, id int64) (CostumeItem, error) {
	item, err := scanCostumeItem(r.db.db.QueryRowContext(ctx,
		`SELECT `+costumeItemColumns+` FROM costume_items WHERE production_id = ? AND id = ?`, productionID, id,
	))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return CostumeItem{}, notFound("costume item")
		}
		return CostumeItem{}, fmt.Errorf("storage: get costume item: %w", err)
	}
	return item, nil
}

func (r *costumeItemRepository) List(ctx context.Context, filter CostumeItemFilter) ([]CostumeItem, error) {
	query := `SELECT ` + costumeItemColumns + ` FROM costume_items WHERE production_id = ?`
	args := []any{filter.ProductionID}
	if !filter.IncludeArchived {
		query += ` AND archived_at IS NULL`
	}
	if filter.ActorID != 0 {
		query += ` AND actor_id = ?`
		args = append(args, filter.ActorID)
	}
	if filter.ItemTypeID != 0 {
		query += ` AND item_type_id = ?`
		args = append(args, filter.ItemTypeID)
	}
	if filter.Code != "" {
		query += ` AND code = ?`
		args = append(args, filter.Code)
	}
	if filter.Status != "" {
		query += ` AND status = ?`
		args = append(args, filter.Status)
	}
	if filter.Incomplete {
		query += ` AND status <> ?`
		args = append(args, StatusComplete)
	}
	if filter.Blocked {
		query += ` AND length(trim(blocker)) > 0`
	}
	query += ` ORDER BY CASE status
		WHEN 'Find' THEN 1
		WHEN 'Make' THEN 2
		WHEN 'Fit' THEN 3
		WHEN 'Alterations' THEN 4
		WHEN 'Complete' THEN 5
		ELSE 6
	END, id`
	rows, err := r.db.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("storage: list costume items: %w", err)
	}
	defer rows.Close()
	result := make([]CostumeItem, 0)
	for rows.Next() {
		item, err := scanCostumeItem(rows)
		if err != nil {
			return nil, fmt.Errorf("storage: scan costume item: %w", err)
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list costume items: %w", err)
	}
	return result, nil
}

func (r *costumeItemRepository) Update(ctx context.Context, input UpdateCostumeItemInput) (CostumeItem, error) {
	tx, err := r.db.begin(ctx)
	if err != nil {
		return CostumeItem{}, err
	}
	defer tx.Rollback()
	if err := ensureProduction(ctx, tx, input.ProductionID); err != nil {
		return CostumeItem{}, err
	}
	if err := ensureActor(ctx, tx, input.ProductionID, input.ActorID); err != nil {
		return CostumeItem{}, err
	}
	if err := ensureItemType(ctx, tx, input.ProductionID, input.ItemTypeID); err != nil {
		return CostumeItem{}, err
	}
	result, err := tx.ExecContext(ctx,
		`UPDATE costume_items SET actor_id = ?, item_type_id = ?, description = ?, status = ?, progress = ?, next_action = ?, blocker = ?, notes = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		 WHERE production_id = ? AND id = ?`,
		input.ActorID, input.ItemTypeID, input.Description, input.Status, input.Progress, input.NextAction, input.Blocker, input.Notes,
		input.ProductionID, input.ID,
	)
	if err != nil {
		return CostumeItem{}, fmt.Errorf("storage: update costume item: %w", err)
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return CostumeItem{}, notFound("costume item")
	}
	item, err := scanCostumeItem(tx.QueryRowContext(ctx,
		`SELECT `+costumeItemColumns+` FROM costume_items WHERE production_id = ? AND id = ?`, input.ProductionID, input.ID,
	))
	if err != nil {
		return CostumeItem{}, fmt.Errorf("storage: read costume item: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return CostumeItem{}, fmt.Errorf("storage: commit costume item: %w", err)
	}
	return item, nil
}

func (r *costumeItemRepository) Archive(ctx context.Context, productionID, id int64) error {
	result, err := r.db.db.ExecContext(ctx,
		`UPDATE costume_items SET archived_at = COALESCE(archived_at, strftime('%Y-%m-%dT%H:%M:%fZ', 'now')), updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE production_id = ? AND id = ?`,
		productionID, id,
	)
	if err != nil {
		return fmt.Errorf("storage: archive costume item: %w", err)
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return notFound("costume item")
	}
	return nil
}
