package storage

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ReportFilter selects active production items for a report.
type ReportFilter struct {
	ProductionID int64
	Status       string
}

// ReportCategory is one item-type count in a filtered report.
type ReportCategory struct {
	ItemTypeID   int64
	ItemTypeName string
	Count        int
}

// ReportAccessory is an accessory item included in the report detail list.
type ReportAccessory struct {
	Code        string
	Description string
	ActorName   string
}

// FilteredReport contains the category counts and accessory details for one
// production and status filter.
type FilteredReport struct {
	Categories  []ReportCategory
	Accessories []ReportAccessory
}

// ReportRepository reads production-scoped filtered report data.
type ReportRepository interface {
	Generate(context.Context, ReportFilter) (FilteredReport, error)
}

type reportRepository struct{ db *DB }

// NewReportRepository constructs filtered report reads over db.
func NewReportRepository(db *DB) ReportRepository {
	return &reportRepository{db: db}
}

// Generate returns active item counts by type and accessory details. Empty
// Status means all workflow statuses; archived items, types, and actors are
// excluded from both report sections.
func (r *reportRepository) Generate(ctx context.Context, filter ReportFilter) (FilteredReport, error) {
	if r == nil || r.db == nil || r.db.db == nil {
		return FilteredReport{}, errors.New("storage: database is closed")
	}
	if filter.ProductionID <= 0 {
		return FilteredReport{}, errors.New("storage: production id is required")
	}

	statusClause := ""
	statusArgs := []any{filter.ProductionID}
	if status := strings.TrimSpace(filter.Status); status != "" {
		statusClause = " AND costume_items.status = ?"
		statusArgs = append(statusArgs, status)
	}

	rows, err := r.db.db.QueryContext(ctx, `
		SELECT item_types.id, item_types.name, COUNT(*)
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
			AND actors.archived_at IS NULL`+statusClause+`
		GROUP BY item_types.id, item_types.name
		ORDER BY item_types.name, item_types.id`, statusArgs...)
	if err != nil {
		return FilteredReport{}, fmt.Errorf("storage: list report categories: %w", err)
	}
	defer rows.Close()

	report := FilteredReport{Categories: make([]ReportCategory, 0)}
	for rows.Next() {
		var category ReportCategory
		if err := rows.Scan(&category.ItemTypeID, &category.ItemTypeName, &category.Count); err != nil {
			return FilteredReport{}, fmt.Errorf("storage: scan report category: %w", err)
		}
		report.Categories = append(report.Categories, category)
	}
	if err := rows.Err(); err != nil {
		return FilteredReport{}, fmt.Errorf("storage: list report categories: %w", err)
	}

	accessoryArgs := []any{filter.ProductionID}
	if status := strings.TrimSpace(filter.Status); status != "" {
		accessoryArgs = append(accessoryArgs, status)
	}
	accessoryRows, err := r.db.db.QueryContext(ctx, `
		SELECT costume_items.code, costume_items.description, actors.name
		FROM costume_items
		JOIN item_types
			ON item_types.production_id = costume_items.production_id
			AND item_types.id = costume_items.item_type_id
		JOIN actors
			ON actors.production_id = costume_items.production_id
			AND actors.id = costume_items.actor_id
		WHERE costume_items.production_id = ?
			AND costume_items.archived_at IS NULL
			AND item_types.archived_at IS NULL
			AND actors.archived_at IS NULL
			AND lower(trim(item_types.name)) = 'accessories'`+statusClause+`
		ORDER BY costume_items.id`, accessoryArgs...)
	if err != nil {
		return FilteredReport{}, fmt.Errorf("storage: list report accessories: %w", err)
	}
	defer accessoryRows.Close()

	report.Accessories = make([]ReportAccessory, 0)
	for accessoryRows.Next() {
		var accessory ReportAccessory
		if err := accessoryRows.Scan(&accessory.Code, &accessory.Description, &accessory.ActorName); err != nil {
			return FilteredReport{}, fmt.Errorf("storage: scan report accessory: %w", err)
		}
		report.Accessories = append(report.Accessories, accessory)
	}
	if err := accessoryRows.Err(); err != nil {
		return FilteredReport{}, fmt.Errorf("storage: list report accessories: %w", err)
	}
	return report, nil
}

var _ ReportRepository = (*reportRepository)(nil)
