package storage

import (
	"context"
	"errors"
	"fmt"
)

// ItemTypeSummary is the production-scoped status breakdown for one active
// item type. Summaries are represented-only: item types with no active items
// are intentionally omitted because they do not contribute to inventory work.
type ItemTypeSummary struct {
	ItemTypeID   int64
	ItemTypeName string
	Total        int
	Complete     int
	InProgress   int
	Blocked      int
	Ready        int
	NotStarted   int
}

// ItemTypeSummaryRepository reads aggregate status counts for a production.
type ItemTypeSummaryRepository interface {
	List(context.Context, int64) ([]ItemTypeSummary, error)
}

type itemTypeSummaryRepository struct{ db *DB }

// NewItemTypeSummaryRepository constructs the production summary repository.
func NewItemTypeSummaryRepository(db *DB) ItemTypeSummaryRepository {
	return &itemTypeSummaryRepository{db: db}
}

// List returns one row per active item type represented by active costume
// items belonging to active actors in the requested production. This is one
// bounded aggregate query; archived item types, items, and actors are excluded.
func (r *itemTypeSummaryRepository) List(ctx context.Context, productionID int64) ([]ItemTypeSummary, error) {
	if r.db == nil || r.db.db == nil {
		return nil, errors.New("storage: database is closed")
	}
	rows, err := r.db.db.QueryContext(ctx, `
		SELECT
			item_types.id,
			item_types.name,
			COUNT(*),
			SUM(CASE WHEN costume_items.status = ? THEN 1 ELSE 0 END),
			SUM(CASE WHEN costume_items.status = ? THEN 1 ELSE 0 END),
			SUM(CASE WHEN costume_items.status = ? THEN 1 ELSE 0 END),
			SUM(CASE WHEN costume_items.status = ? THEN 1 ELSE 0 END),
			SUM(CASE WHEN costume_items.status = ? THEN 1 ELSE 0 END)
		FROM item_types
		JOIN costume_items
			ON costume_items.production_id = item_types.production_id
			AND costume_items.item_type_id = item_types.id
		JOIN actors
			ON actors.production_id = costume_items.production_id
			AND actors.id = costume_items.actor_id
		WHERE item_types.production_id = ?
			AND item_types.archived_at IS NULL
			AND costume_items.archived_at IS NULL
			AND actors.archived_at IS NULL
		GROUP BY item_types.id, item_types.name
		ORDER BY item_types.name, item_types.id`,
		StatusComplete, StatusInProgress, StatusBlocked, StatusReady, StatusNotStarted, productionID,
	)
	if err != nil {
		return nil, fmt.Errorf("storage: list item type summary: %w", err)
	}
	defer rows.Close()

	result := make([]ItemTypeSummary, 0)
	for rows.Next() {
		var row ItemTypeSummary
		if err := rows.Scan(&row.ItemTypeID, &row.ItemTypeName, &row.Total, &row.Complete, &row.InProgress, &row.Blocked, &row.Ready, &row.NotStarted); err != nil {
			return nil, fmt.Errorf("storage: scan item type summary: %w", err)
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list item type summary: %w", err)
	}
	return result, nil
}

var _ ItemTypeSummaryRepository = (*itemTypeSummaryRepository)(nil)
