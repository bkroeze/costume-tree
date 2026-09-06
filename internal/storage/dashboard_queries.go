package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ProductionKPIs is the bounded, production-scoped rollup used by the
// dashboard. Workflow counts come from status, while Blocked is independently
// derived from a non-empty blocker note.
type ProductionKPIs struct {
	TotalActivePieces int
	Find              int
	Make              int
	Fit               int
	Alterations       int
	Complete          int
	Blocked           int
	Incomplete        int
}

// ActorSummary is one row from the actor aggregate. Completion is nil when an
// active actor has no active items; callers can render that state as an em
// dash instead of manufacturing a percentage.
type ActorSummary struct {
	ID              int64
	ProductionID    int64
	Name            string
	Role            string
	ActiveItems     int
	Find            int
	Make            int
	Fit             int
	Alterations     int
	Complete        int
	Blocked         int
	Incomplete      int
	Completion      *float64
	CompletionLabel string
}

// DashboardQueries provides aggregate reads for a production dashboard. Each
// method performs one production-scoped SQL query and excludes archived
// actors/items.
type DashboardQueries interface {
	ProductionKPIs(context.Context, int64) (ProductionKPIs, error)
	ActorSummaries(context.Context, int64) ([]ActorSummary, error)
}

type dashboardQueries struct{ db *DB }

// NewDashboardQueries constructs aggregate dashboard reads over db.
func NewDashboardQueries(db *DB) DashboardQueries { return &dashboardQueries{db: db} }

// NewDashboardQueryRepository is a descriptive alias for composition roots.
func NewDashboardQueryRepository(db *DB) DashboardQueries { return NewDashboardQueries(db) }

func (q *dashboardQueries) ProductionKPIs(ctx context.Context, productionID int64) (ProductionKPIs, error) {
	if q == nil || q.db == nil || q.db.db == nil {
		return ProductionKPIs{}, errors.New("storage: database is closed")
	}
	const statement = `
SELECT
  COUNT(ci.id),
  COALESCE(SUM(CASE WHEN ci.status = ? THEN 1 ELSE 0 END), 0),
  COALESCE(SUM(CASE WHEN ci.status = ? THEN 1 ELSE 0 END), 0),
  COALESCE(SUM(CASE WHEN ci.status = ? THEN 1 ELSE 0 END), 0),
  COALESCE(SUM(CASE WHEN ci.status = ? THEN 1 ELSE 0 END), 0),
  COALESCE(SUM(CASE WHEN ci.status = ? THEN 1 ELSE 0 END), 0),
  COALESCE(SUM(CASE WHEN length(trim(ci.blocker)) > 0 THEN 1 ELSE 0 END), 0)
FROM actors a
LEFT JOIN costume_items ci
  ON ci.production_id = a.production_id
 AND ci.actor_id = a.id
 AND ci.archived_at IS NULL
WHERE a.production_id = ?
  AND a.archived_at IS NULL`
	var result ProductionKPIs
	err := q.db.db.QueryRowContext(ctx, statement,
		StatusFind, StatusMake, StatusFit, StatusAlterations, StatusComplete, productionID,
	).Scan(&result.TotalActivePieces, &result.Find, &result.Make, &result.Fit,
		&result.Alterations, &result.Complete, &result.Blocked)
	if err != nil {
		return ProductionKPIs{}, fmt.Errorf("storage: dashboard production kpis: %w", err)
	}
	result.Incomplete = result.TotalActivePieces - result.Complete
	return result, nil
}

func (q *dashboardQueries) ActorSummaries(ctx context.Context, productionID int64) ([]ActorSummary, error) {
	if q == nil || q.db == nil || q.db.db == nil {
		return nil, errors.New("storage: database is closed")
	}
	const statement = `
SELECT
  a.id,
  a.production_id,
  a.name,
  a.role,
  COUNT(ci.id),
  COALESCE(SUM(CASE WHEN ci.status = ? THEN 1 ELSE 0 END), 0),
  COALESCE(SUM(CASE WHEN ci.status = ? THEN 1 ELSE 0 END), 0),
  COALESCE(SUM(CASE WHEN ci.status = ? THEN 1 ELSE 0 END), 0),
  COALESCE(SUM(CASE WHEN ci.status = ? THEN 1 ELSE 0 END), 0),
  COALESCE(SUM(CASE WHEN ci.status = ? THEN 1 ELSE 0 END), 0),
  COALESCE(SUM(CASE WHEN length(trim(ci.blocker)) > 0 THEN 1 ELSE 0 END), 0),
  AVG(ci.progress)
FROM actors a
LEFT JOIN costume_items ci
  ON ci.production_id = a.production_id
 AND ci.actor_id = a.id
 AND ci.archived_at IS NULL
WHERE a.production_id = ?
  AND a.archived_at IS NULL
GROUP BY a.id, a.production_id, a.name, a.role
ORDER BY lower(a.name), a.id`
	rows, err := q.db.db.QueryContext(ctx, statement,
		StatusFind, StatusMake, StatusFit, StatusAlterations, StatusComplete, productionID,
	)
	if err != nil {
		return nil, fmt.Errorf("storage: dashboard actor summaries: %w", err)
	}
	defer rows.Close()
	result := make([]ActorSummary, 0)
	for rows.Next() {
		var summary ActorSummary
		var average sql.NullFloat64
		if err := rows.Scan(&summary.ID, &summary.ProductionID, &summary.Name, &summary.Role,
			&summary.ActiveItems, &summary.Find, &summary.Make, &summary.Fit,
			&summary.Alterations, &summary.Complete, &summary.Blocked, &average); err != nil {
			return nil, fmt.Errorf("storage: scan dashboard actor summary: %w", err)
		}
		summary.Incomplete = summary.ActiveItems - summary.Complete
		if average.Valid {
			value := average.Float64
			summary.Completion = &value
			summary.CompletionLabel = fmt.Sprintf("%.0f%%", value)
		}
		result = append(result, summary)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: dashboard actor summaries: %w", err)
	}
	return result, nil
}
