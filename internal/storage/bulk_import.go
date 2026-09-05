package storage

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	bulkinput "costume-tree/internal/bulk"
)

const BulkImportPreviewTTL = 10 * time.Minute

var (
	ErrBulkStalePreview    = errors.New("storage: bulk import preview is stale")
	ErrBulkDuplicateSubmit = errors.New("storage: bulk import was already committed")
	ErrBulkUnknownType     = errors.New("storage: bulk import contains an unknown item type")
	ErrBulkInvalidPreview  = errors.New("storage: invalid bulk import preview")
)

const (
	BulkStateExisting  = "existing"
	BulkStateNew       = "new"
	BulkStateKnown     = "known"
	BulkStateUnknown   = "unknown"
	BulkStateDuplicate = "duplicate"
	BulkStateError     = "error"
)

// BulkImportRow is the canonical, production-scoped interpretation of one
// actor/item line. ActorLine and ItemLine refer to the original pasted input.
type BulkImportRow struct {
	Number    int
	Block     int
	ActorLine int
	ItemLine  int

	// ActorName and ItemTypeName are canonical names used at commit time.
	// Actor and ItemType are compatibility aliases for presentation callers.
	ActorName      string
	ItemTypeName   string
	Actor          string
	ItemType       string
	ActorID        int64
	ItemTypeID     int64
	ActorState     string
	ItemTypeState  string
	ActorStatus    string
	ItemTypeStatus string
	Status         string
	Duplicate      bool
	Error          string
	Code           string
}

// BulkImportPreview is an immutable snapshot produced by Preview. Web callers
// should retain it server-side and never rebuild it from submitted form fields.
type BulkImportPreview struct {
	ID             string
	ProductionID   int64
	ProductionName string
	AllowNewTypes  bool
	CreatedAt      time.Time
	Rows           []BulkImportRow
	Errors         []string
	Valid          bool
}

// BulkImporter performs import previews and commits against one SQLite DB.
type BulkImporter struct {
	db *DB

	mu        sync.Mutex
	inflight  map[string]struct{}
	committed map[string]time.Time
}

func NewBulkImporter(db *DB) *BulkImporter {
	return &BulkImporter{db: db, inflight: make(map[string]struct{}), committed: make(map[string]time.Time)}
}

// Preview resolves actor and item-type names without writing anything.
func (i *BulkImporter) Preview(ctx context.Context, productionID int64, blocks []bulkinput.Block, allowNewTypes bool) (BulkImportPreview, error) {
	if i == nil || i.db == nil || i.db.db == nil {
		return BulkImportPreview{}, errors.New("storage: bulk importer database is required")
	}
	productionName, actors, types, err := i.scope(ctx, productionID)
	if err != nil {
		return BulkImportPreview{}, err
	}
	preview := BulkImportPreview{ID: newBulkPreviewID(), ProductionID: productionID, ProductionName: productionName, AllowNewTypes: allowNewTypes, CreatedAt: time.Now().UTC(), Valid: true}
	if len(blocks) == 0 {
		preview.Valid = false
		preview.Errors = append(preview.Errors, "Paste at least one actor and item type.")
		return preview, nil
	}

	actorByName := make(map[string]Actor, len(actors))
	for _, actor := range actors {
		if _, exists := actorByName[bulkNameKey(actor.Name)]; !exists {
			actorByName[bulkNameKey(actor.Name)] = actor
		}
	}
	typeByName := make(map[string]ItemType, len(types))
	for _, typ := range types {
		typeByName[bulkNameKey(typ.Name)] = typ
	}
	seenPairs := make(map[string]bool)
	rowNumber := 0
	for blockNumber, block := range blocks {
		if strings.TrimSpace(block.Actor) == "" || len(block.Items) == 0 {
			preview.Valid = false
			preview.Errors = append(preview.Errors, fmt.Sprintf("Block %d is missing an actor or item type.", blockNumber+1))
			continue
		}
		actorName := strings.TrimSpace(block.Actor)
		actorKey := bulkNameKey(actorName)
		actor, actorKnown := actorByName[actorKey]
		for _, item := range block.Items {
			rowNumber++
			itemName := strings.TrimSpace(item.Type)
			row := BulkImportRow{Number: rowNumber, Block: blockNumber + 1, ActorLine: block.ActorLine, ItemLine: item.Line, ActorName: actorName, ItemTypeName: itemName, Actor: actorName, ItemType: itemName, Status: "ok"}
			if actorKnown {
				row.ActorName, row.Actor, row.ActorID = actor.Name, actor.Name, actor.ID
				row.ActorState, row.ActorStatus = BulkStateExisting, BulkStateExisting
			} else {
				row.ActorState, row.ActorStatus = BulkStateNew, BulkStateNew
			}

			typ, typeKnown := typeByName[bulkNameKey(itemName)]
			if typeKnown {
				row.ItemTypeName, row.ItemType = typ.Name, typ.Name
				row.ItemTypeID = typ.ID
				row.ItemTypeState, row.ItemTypeStatus = BulkStateKnown, BulkStateKnown
			} else if allowNewTypes {
				row.ItemTypeState, row.ItemTypeStatus = BulkStateNew, BulkStateNew
			} else {
				row.ItemTypeState, row.ItemTypeStatus = BulkStateUnknown, BulkStateUnknown
				row.Status, row.Error = BulkStateError, fmt.Sprintf("Unknown item type %q; select allow-new-types to create it.", itemName)
				preview.Valid = false
				preview.Errors = append(preview.Errors, fmt.Sprintf("Line %d: %s", item.Line, row.Error))
			}
			pairKey := actorKey + "\x00" + bulkNameKey(itemName)
			if seenPairs[pairKey] {
				row.Duplicate = true
				if row.Status == "ok" {
					row.Status = BulkStateDuplicate
				}
			} else {
				seenPairs[pairKey] = true
			}
			if row.Status == "ok" && (row.ActorState == BulkStateNew || row.ItemTypeState == BulkStateNew) {
				row.Status = BulkStateNew
			}
			preview.Rows = append(preview.Rows, row)
		}
	}
	if len(preview.Rows) == 0 {
		preview.Valid = false
		preview.Errors = append(preview.Errors, "Paste at least one actor and item type.")
	}
	return preview, nil
}

// PreviewImport is a descriptive alias for Preview.
func (i *BulkImporter) PreviewImport(ctx context.Context, productionID int64, blocks []bulkinput.Block, allowNewTypes bool) (BulkImportPreview, error) {
	return i.Preview(ctx, productionID, blocks, allowNewTypes)
}

// Commit writes the retained preview in one transaction. Names are resolved
// again inside that transaction, so a preview cannot write into a renamed or
// otherwise changed production scope.
func (i *BulkImporter) Commit(ctx context.Context, preview BulkImportPreview) ([]CostumeItem, error) {
	if i == nil || i.db == nil || i.db.db == nil {
		return nil, errors.New("storage: bulk importer database is required")
	}
	if preview.ID == "" || preview.ProductionID <= 0 || preview.CreatedAt.IsZero() || len(preview.Rows) == 0 || !preview.Valid {
		return nil, ErrBulkInvalidPreview
	}
	if age := time.Since(preview.CreatedAt); age < -time.Minute || age > BulkImportPreviewTTL {
		return nil, ErrBulkStalePreview
	}

	i.mu.Lock()
	now := time.Now().UTC()
	for id, at := range i.committed {
		if now.Sub(at) > BulkImportPreviewTTL {
			delete(i.committed, id)
		}
	}
	if _, ok := i.committed[preview.ID]; ok {
		i.mu.Unlock()
		return nil, ErrBulkDuplicateSubmit
	}
	if _, ok := i.inflight[preview.ID]; ok {
		i.mu.Unlock()
		return nil, ErrBulkDuplicateSubmit
	}
	i.inflight[preview.ID] = struct{}{}
	i.mu.Unlock()
	defer func() {
		i.mu.Lock()
		delete(i.inflight, preview.ID)
		i.mu.Unlock()
	}()

	tx, err := i.db.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var productionName, archived string
	if err := tx.QueryRowContext(ctx, `SELECT name, COALESCE(archived_at, '') FROM productions WHERE id = ?`, preview.ProductionID).Scan(&productionName, &archived); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: production", ErrBulkStalePreview)
		}
		return nil, fmt.Errorf("storage: validate bulk production: %w", err)
	}
	if archived != "" || !strings.EqualFold(strings.TrimSpace(productionName), strings.TrimSpace(preview.ProductionName)) {
		return nil, ErrBulkStalePreview
	}

	actors, types, err := i.scopeTx(ctx, tx, preview.ProductionID)
	if err != nil {
		return nil, err
	}
	actorByName := make(map[string]Actor, len(actors))
	for _, actor := range actors {
		if _, exists := actorByName[bulkNameKey(actor.Name)]; !exists {
			actorByName[bulkNameKey(actor.Name)] = actor
		}
	}
	typeByName := make(map[string]ItemType, len(types))
	for _, typ := range types {
		typeByName[bulkNameKey(typ.Name)] = typ
	}
	result := make([]CostumeItem, 0, len(preview.Rows))
	for rowIndex, row := range preview.Rows {
		actorName := strings.TrimSpace(row.ActorName)
		if actorName == "" {
			actorName = strings.TrimSpace(row.Actor)
		}
		itemName := strings.TrimSpace(row.ItemTypeName)
		if itemName == "" {
			itemName = strings.TrimSpace(row.ItemType)
		}
		if actorName == "" || itemName == "" {
			return nil, fmt.Errorf("%w: row %d has an empty actor or item type", ErrBulkInvalidPreview, rowIndex+1)
		}
		actorKey, typeKey := bulkNameKey(actorName), bulkNameKey(itemName)
		actor, ok := actorByName[actorKey]
		if !ok {
			created, createErr := insertBulkActor(ctx, tx, preview.ProductionID, actorName)
			if createErr != nil {
				return nil, createErr
			}
			actor = created
			actorByName[actorKey] = actor
		}
		typ, ok := typeByName[typeKey]
		if !ok {
			if !preview.AllowNewTypes {
				return nil, fmt.Errorf("%w: %s", ErrBulkUnknownType, itemName)
			}
			created, createErr := insertBulkItemType(ctx, tx, preview.ProductionID, itemName)
			if createErr != nil {
				return nil, createErr
			}
			typ = created
			typeByName[typeKey] = typ
		}
		code, allocErr := allocateCodeTx(ctx, tx, preview.ProductionID)
		if allocErr != nil {
			return nil, allocErr
		}
		insert, insertErr := tx.ExecContext(ctx, `INSERT INTO costume_items (production_id, actor_id, item_type_id, code) VALUES (?, ?, ?, ?)`, preview.ProductionID, actor.ID, typ.ID, code)
		if insertErr != nil {
			return nil, fmt.Errorf("storage: bulk create costume item: %w", insertErr)
		}
		id, idErr := insert.LastInsertId()
		if idErr != nil {
			return nil, fmt.Errorf("storage: bulk create costume item id: %w", idErr)
		}
		item, scanErr := scanCostumeItem(tx.QueryRowContext(ctx, `SELECT `+costumeItemColumns+` FROM costume_items WHERE production_id = ? AND id = ?`, preview.ProductionID, id))
		if scanErr != nil {
			return nil, fmt.Errorf("storage: bulk read costume item: %w", scanErr)
		}
		result = append(result, item)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("storage: commit bulk import: %w", err)
	}
	i.mu.Lock()
	i.committed[preview.ID] = time.Now().UTC()
	i.mu.Unlock()
	return result, nil
}

// CommitImport is a descriptive alias for Commit.
func (i *BulkImporter) CommitImport(ctx context.Context, preview BulkImportPreview) ([]CostumeItem, error) {
	return i.Commit(ctx, preview)
}

func (i *BulkImporter) scope(ctx context.Context, productionID int64) (string, []Actor, []ItemType, error) {
	var productionName, archived string
	if err := i.db.db.QueryRowContext(ctx, `SELECT name, COALESCE(archived_at, '') FROM productions WHERE id = ?`, productionID).Scan(&productionName, &archived); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil, nil, notFound("production")
		}
		return "", nil, nil, fmt.Errorf("storage: get bulk production: %w", err)
	}
	if archived != "" {
		return "", nil, nil, ErrArchived
	}
	actors, err := listBulkActors(ctx, i.db.db, productionID)
	if err != nil {
		return "", nil, nil, err
	}
	types, err := listBulkItemTypes(ctx, i.db.db, productionID)
	if err != nil {
		return "", nil, nil, err
	}
	return productionName, actors, types, nil
}

func (i *BulkImporter) scopeTx(ctx context.Context, tx *sql.Tx, productionID int64) ([]Actor, []ItemType, error) {
	actors, err := listBulkActors(ctx, tx, productionID)
	if err != nil {
		return nil, nil, err
	}
	types, err := listBulkItemTypes(ctx, tx, productionID)
	if err != nil {
		return nil, nil, err
	}
	return actors, types, nil
}

type bulkQuery interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func listBulkActors(ctx context.Context, query bulkQuery, productionID int64) ([]Actor, error) {
	rows, err := query.QueryContext(ctx, `SELECT `+actorColumns+` FROM actors WHERE production_id = ? AND archived_at IS NULL ORDER BY id`, productionID)
	if err != nil {
		return nil, fmt.Errorf("storage: list bulk actors: %w", err)
	}
	defer rows.Close()
	result := make([]Actor, 0)
	for rows.Next() {
		actor, scanErr := scanActor(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("storage: scan bulk actor: %w", scanErr)
		}
		result = append(result, actor)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list bulk actors: %w", err)
	}
	return result, nil
}

func listBulkItemTypes(ctx context.Context, query bulkQuery, productionID int64) ([]ItemType, error) {
	rows, err := query.QueryContext(ctx, `SELECT `+itemTypeColumns+` FROM item_types WHERE production_id = ? AND archived_at IS NULL ORDER BY id`, productionID)
	if err != nil {
		return nil, fmt.Errorf("storage: list bulk item types: %w", err)
	}
	defer rows.Close()
	result := make([]ItemType, 0)
	for rows.Next() {
		typ, scanErr := scanItemType(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("storage: scan bulk item type: %w", scanErr)
		}
		result = append(result, typ)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list bulk item types: %w", err)
	}
	return result, nil
}

func insertBulkActor(ctx context.Context, tx *sql.Tx, productionID int64, name string) (Actor, error) {
	result, err := tx.ExecContext(ctx, `INSERT INTO actors (production_id, name) VALUES (?, ?)`, productionID, name)
	if err != nil {
		return Actor{}, fmt.Errorf("storage: bulk create actor: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return Actor{}, fmt.Errorf("storage: bulk create actor id: %w", err)
	}
	actor, err := scanActor(tx.QueryRowContext(ctx, `SELECT `+actorColumns+` FROM actors WHERE production_id = ? AND id = ?`, productionID, id))
	if err != nil {
		return Actor{}, fmt.Errorf("storage: bulk read actor: %w", err)
	}
	return actor, nil
}

func insertBulkItemType(ctx context.Context, tx *sql.Tx, productionID int64, name string) (ItemType, error) {
	result, err := tx.ExecContext(ctx, `INSERT INTO item_types (production_id, name) VALUES (?, ?)`, productionID, name)
	if err != nil {
		return ItemType{}, fmt.Errorf("storage: bulk create item type: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return ItemType{}, fmt.Errorf("storage: bulk create item type id: %w", err)
	}
	typ, err := scanItemType(tx.QueryRowContext(ctx, `SELECT `+itemTypeColumns+` FROM item_types WHERE production_id = ? AND id = ?`, productionID, id))
	if err != nil {
		return ItemType{}, fmt.Errorf("storage: bulk read item type: %w", err)
	}
	return typ, nil
}

func bulkNameKey(value string) string { return strings.ToLower(strings.TrimSpace(value)) }

func newBulkPreviewID() string {
	var data [24]byte
	if _, err := rand.Read(data[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(data[:])
}
