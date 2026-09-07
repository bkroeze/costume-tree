package storage

import (
	"context"
	"errors"
	"fmt"
)

type ProductionDirectoryEntry struct {
	ID           int64
	Name         string
	ActorCount   int
	PieceCount   int
	BlockedCount int
}

type ProductionDirectoryQueries interface {
	ProductionDirectory(context.Context) ([]ProductionDirectoryEntry, error)
}

type productionDirectoryQueries struct{ db *DB }

func NewProductionDirectoryQueries(db *DB) ProductionDirectoryQueries {
	return &productionDirectoryQueries{db: db}
}

func (q *productionDirectoryQueries) ProductionDirectory(ctx context.Context) ([]ProductionDirectoryEntry, error) {
	if q == nil || q.db == nil || q.db.db == nil {
		return nil, errors.New("storage: database is closed")
	}
	const statement = `
SELECT
  p.id,
  p.name,
  COUNT(DISTINCT a.id),
  COUNT(ci.id),
  COALESCE(SUM(CASE WHEN length(trim(ci.blocker)) > 0 THEN 1 ELSE 0 END), 0)
FROM productions p
LEFT JOIN actors a
  ON a.production_id = p.id
 AND a.archived_at IS NULL
LEFT JOIN costume_items ci
  ON ci.production_id = p.id
 AND ci.actor_id = a.id
 AND ci.archived_at IS NULL
WHERE p.archived_at IS NULL
GROUP BY p.id, p.name
ORDER BY lower(p.name), p.id`
	rows, err := q.db.db.QueryContext(ctx, statement)
	if err != nil {
		return nil, fmt.Errorf("storage: production directory: %w", err)
	}
	defer rows.Close()
	entries := make([]ProductionDirectoryEntry, 0)
	for rows.Next() {
		var entry ProductionDirectoryEntry
		if err := rows.Scan(&entry.ID, &entry.Name, &entry.ActorCount, &entry.PieceCount, &entry.BlockedCount); err != nil {
			return nil, fmt.Errorf("storage: scan production directory: %w", err)
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: production directory: %w", err)
	}
	return entries, nil
}
