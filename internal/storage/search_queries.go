package storage

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ItemSearchFilter is the production-scoped filter set for item search. A zero
// value leaves a filter unset. Results are always bounded; Limit and Offset
// provide stable page navigation.
type ItemSearchFilter struct {
	ProductionID    int64
	Code            string
	ActorID         int64
	ItemTypeID      int64
	Status          string
	Blocked         bool
	Incomplete      bool
	MinProgress     *int
	MaxProgress     *int
	Text            string
	IncludeArchived bool
	Limit           int
	Offset          int
}

// SearchFilter is retained as a concise name for callers that use the query as
// a production search object.
type SearchFilter = ItemSearchFilter

// ItemSearchResult contains only the fields needed by production-wide search
// results. Joins are performed in the query so rendering never needs an
// N+1 lookup for actor or item-type names.
type ItemSearchResult struct {
	ID          int64
	ItemID      int64
	Code        string
	Actor       string
	ActorName   string
	Role        string
	Type        string
	ItemType    string
	Description string
	Status      string
	Progress    int
	NextAction  string
	Blocker     string
}

// SearchResult is an alternate name for ItemSearchResult.
type SearchResult = ItemSearchResult

// ItemSearchRepository executes bounded, production-scoped item searches.
type ItemSearchRepository interface {
	Search(context.Context, ItemSearchFilter) ([]ItemSearchResult, error)
}

// ItemSearch is the concrete query object. It is intentionally separate from
// CostumeItemRepository because search reads denormalized display fields.
type ItemSearch struct{ db *DB }

// NewItemSearch constructs a production-wide item search query object.
func NewItemSearch(db *DB) *ItemSearch { return &ItemSearch{db: db} }

// NewItemSearchRepository is the repository-shaped constructor used by web
// composition roots.
func NewItemSearchRepository(db *DB) ItemSearchRepository { return NewItemSearch(db) }

const (
	DefaultItemSearchLimit = 50
	MaxItemSearchLimit     = 100
)

// Search returns a stable, bounded page of active items in one production.
func (q *ItemSearch) Search(ctx context.Context, filter ItemSearchFilter) ([]ItemSearchResult, error) {
	if q == nil || q.db == nil || q.db.db == nil {
		return nil, errors.New("storage: database is closed")
	}
	if filter.ProductionID <= 0 {
		return nil, errors.New("storage: production id is required")
	}

	limit := filter.Limit
	if limit <= 0 {
		limit = DefaultItemSearchLimit
	}
	if limit > MaxItemSearchLimit {
		limit = MaxItemSearchLimit
	}
	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}

	query := `SELECT ci.code, a.name, a.role, it.name, ci.description, ci.status,
		ci.progress, ci.next_action, ci.blocker, ci.id
		FROM costume_items AS ci
		JOIN actors AS a ON a.production_id = ci.production_id AND a.id = ci.actor_id
		JOIN item_types AS it ON it.production_id = ci.production_id AND it.id = ci.item_type_id
		WHERE ci.production_id = ?`
	args := []any{filter.ProductionID}
	if !filter.IncludeArchived {
		query += ` AND ci.archived_at IS NULL AND a.archived_at IS NULL`
	}
	if code := normalizeSearchCode(filter.Code); code != "" {
		// Codes allocated by the repository are canonical uppercase values. The
		// input normalization keeps this equality predicate index-friendly.
		query += ` AND ci.code = ?`
		args = append(args, code)
	}
	if filter.ActorID > 0 {
		query += ` AND ci.actor_id = ?`
		args = append(args, filter.ActorID)
	}
	if filter.ItemTypeID > 0 {
		query += ` AND ci.item_type_id = ?`
		args = append(args, filter.ItemTypeID)
	}
	if status := strings.TrimSpace(filter.Status); status != "" {
		query += ` AND ci.status = ?`
		args = append(args, status)
	}
	if filter.Blocked {
		query += ` AND length(trim(ci.blocker)) > 0`
	}
	if filter.Incomplete {
		query += ` AND ci.status <> ?`
		args = append(args, StatusComplete)
	}
	if filter.MinProgress != nil {
		query += ` AND ci.progress >= ?`
		args = append(args, *filter.MinProgress)
	}
	if filter.MaxProgress != nil {
		query += ` AND ci.progress <= ?`
		args = append(args, *filter.MaxProgress)
	}
	if text := strings.ToLower(strings.TrimSpace(filter.Text)); text != "" {
		// instr() makes search literal and predictable: wildcard characters in
		// user text are not interpreted as SQL patterns.
		query += ` AND (instr(lower(a.name), ?) > 0 OR instr(lower(a.role), ?) > 0 OR instr(lower(it.name), ?) > 0 OR instr(lower(ci.description), ?) > 0)`
		args = append(args, text, text, text, text)
	}
	query += ` ORDER BY CASE ci.status
		WHEN 'Find' THEN 1
		WHEN 'Make' THEN 2
		WHEN 'Fit' THEN 3
		WHEN 'Alterations' THEN 4
		WHEN 'Complete' THEN 5
		ELSE 6
	END, ci.id LIMIT ? OFFSET ?`
	args = append(args, limit, offset)

	rows, err := q.db.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("storage: search costume items: %w", err)
	}
	defer rows.Close()
	result := make([]ItemSearchResult, 0, limit)
	for rows.Next() {
		var item ItemSearchResult
		if err := rows.Scan(&item.Code, &item.Actor, &item.Role, &item.Type, &item.Description, &item.Status, &item.Progress, &item.NextAction, &item.Blocker, &item.ID); err != nil {
			return nil, fmt.Errorf("storage: scan costume item search: %w", err)
		}
		item.ItemID = item.ID
		item.ActorName = item.Actor
		item.ItemType = item.Type
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: search costume items: %w", err)
	}
	return result, nil
}

// Query is a convenience alias for callers treating ItemSearch as a query
// object rather than a repository.
func (q *ItemSearch) Query(ctx context.Context, filter ItemSearchFilter) ([]ItemSearchResult, error) {
	return q.Search(ctx, filter)
}

func normalizeSearchCode(value string) string {
	return strings.ToUpper(strings.TrimSpace(value))
}
