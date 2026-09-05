package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

type costumeItemPhotoRepository struct{ db *DB }

func NewCostumeItemPhotoRepository(db *DB) CostumeItemPhotoRepository {
	return &costumeItemPhotoRepository{db: db}
}

const costumeItemPhotoColumns = `id, production_id, costume_item_id, original_name, display_name, thumbnail_name, media_type, status, error_message, created_at, updated_at`

func validatePhotoFilename(field, name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("storage: photo %s is required", field)
	}
	if name == "." || name == ".." || filepath.IsAbs(name) || filepath.Base(name) != name || strings.ContainsAny(name, `/\\`) || strings.IndexByte(name, 0) >= 0 {
		return fmt.Errorf("storage: photo %s must be a safe base name", field)
	}
	return nil
}

func validatePhotoStatus(status string) error {
	switch status {
	case PhotoStatusPending, PhotoStatusReady, PhotoStatusFailed:
		return nil
	default:
		return fmt.Errorf("%w: %q", ErrInvalidPhotoStatus, status)
	}
}

func validatePhotoInput(input CreateCostumeItemPhotoInput) (CreateCostumeItemPhotoInput, error) {
	for _, value := range []struct {
		field string
		name  string
	}{
		{field: "original filename", name: input.OriginalName},
		{field: "display filename", name: input.DisplayName},
		{field: "thumbnail filename", name: input.ThumbnailName},
	} {
		if err := validatePhotoFilename(value.field, value.name); err != nil {
			return CreateCostumeItemPhotoInput{}, err
		}
	}
	input.MediaType = strings.TrimSpace(input.MediaType)
	if input.MediaType == "" {
		return CreateCostumeItemPhotoInput{}, errors.New("storage: photo media type is required")
	}
	return input, nil
}

func scanCostumeItemPhoto(scanner interface{ Scan(...any) error }) (CostumeItemPhoto, error) {
	var photo CostumeItemPhoto
	var created, updated string
	if err := scanner.Scan(
		&photo.ID, &photo.ProductionID, &photo.CostumeItemID,
		&photo.OriginalName, &photo.DisplayName, &photo.ThumbnailName,
		&photo.MediaType, &photo.Status, &photo.ErrorMessage, &created, &updated,
	); err != nil {
		return CostumeItemPhoto{}, err
	}
	if err := validatePhotoFilename("original filename", photo.OriginalName); err != nil {
		return CostumeItemPhoto{}, err
	}
	if err := validatePhotoFilename("display filename", photo.DisplayName); err != nil {
		return CostumeItemPhoto{}, err
	}
	if err := validatePhotoFilename("thumbnail filename", photo.ThumbnailName); err != nil {
		return CostumeItemPhoto{}, err
	}
	if strings.TrimSpace(photo.MediaType) == "" {
		return CostumeItemPhoto{}, errors.New("storage: stored photo media type is empty")
	}
	if err := validatePhotoStatus(photo.Status); err != nil {
		return CostumeItemPhoto{}, err
	}
	var err error
	if photo.CreatedAt, err = parseTimestamp(created); err != nil {
		return CostumeItemPhoto{}, err
	}
	if photo.UpdatedAt, err = parseTimestamp(updated); err != nil {
		return CostumeItemPhoto{}, err
	}
	return photo, nil
}

func ensureCostumeItemForPhoto(ctx context.Context, query rowQuerier, productionID, costumeItemID int64) error {
	var archived string
	if err := query.QueryRowContext(ctx,
		`SELECT COALESCE(archived_at, '') FROM costume_items WHERE production_id = ? AND id = ?`,
		productionID, costumeItemID,
	).Scan(&archived); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return notFound("costume item")
		}
		return err
	}
	if archived != "" {
		return ErrArchived
	}
	return nil
}

func (r *costumeItemPhotoRepository) database() (*sql.DB, error) {
	if r == nil || r.db == nil || r.db.db == nil {
		return nil, errors.New("storage: database is closed")
	}
	return r.db.db, nil
}

func (r *costumeItemPhotoRepository) Create(ctx context.Context, input CreateCostumeItemPhotoInput) (CostumeItemPhoto, error) {
	input, err := validatePhotoInput(input)
	if err != nil {
		return CostumeItemPhoto{}, err
	}
	if _, err := r.database(); err != nil {
		return CostumeItemPhoto{}, err
	}
	tx, err := r.db.begin(ctx)
	if err != nil {
		return CostumeItemPhoto{}, err
	}
	defer tx.Rollback()
	if err := ensureProduction(ctx, tx, input.ProductionID); err != nil {
		return CostumeItemPhoto{}, err
	}
	if err := ensureCostumeItemForPhoto(ctx, tx, input.ProductionID, input.CostumeItemID); err != nil {
		return CostumeItemPhoto{}, err
	}
	result, err := tx.ExecContext(ctx,
		`INSERT INTO costume_item_photos
		 (production_id, costume_item_id, original_name, display_name, thumbnail_name, media_type)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		input.ProductionID, input.CostumeItemID, input.OriginalName,
		input.DisplayName, input.ThumbnailName, input.MediaType,
	)
	if err != nil {
		return CostumeItemPhoto{}, fmt.Errorf("storage: create costume item photo: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return CostumeItemPhoto{}, fmt.Errorf("storage: create costume item photo id: %w", err)
	}
	photo, err := scanCostumeItemPhoto(tx.QueryRowContext(ctx,
		`SELECT `+costumeItemPhotoColumns+` FROM costume_item_photos WHERE id = ?`, id,
	))
	if err != nil {
		return CostumeItemPhoto{}, fmt.Errorf("storage: read costume item photo: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return CostumeItemPhoto{}, fmt.Errorf("storage: commit costume item photo: %w", err)
	}
	return photo, nil
}

func (r *costumeItemPhotoRepository) Get(ctx context.Context, productionID, costumeItemID, photoID int64) (CostumeItemPhoto, error) {
	db, err := r.database()
	if err != nil {
		return CostumeItemPhoto{}, err
	}
	photo, err := scanCostumeItemPhoto(db.QueryRowContext(ctx,
		`SELECT `+costumeItemPhotoColumns+`
		 FROM costume_item_photos
		 WHERE production_id = ? AND costume_item_id = ? AND id = ?`,
		productionID, costumeItemID, photoID,
	))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return CostumeItemPhoto{}, notFound("costume item photo")
		}
		return CostumeItemPhoto{}, fmt.Errorf("storage: get costume item photo: %w", err)
	}
	return photo, nil
}

func (r *costumeItemPhotoRepository) List(ctx context.Context, productionID, costumeItemID int64) ([]CostumeItemPhoto, error) {
	db, err := r.database()
	if err != nil {
		return nil, err
	}
	return listCostumeItemPhotos(ctx, db,
		`SELECT `+costumeItemPhotoColumns+`
		 FROM costume_item_photos
		 WHERE production_id = ? AND costume_item_id = ?
		 ORDER BY created_at, id`,
		[]any{productionID, costumeItemID}, "list costume item photos",
	)
}

func (r *costumeItemPhotoRepository) ListFirstReadyByActor(ctx context.Context, productionID, actorID int64) ([]CostumeItemPhoto, error) {
	db, err := r.database()
	if err != nil {
		return nil, err
	}
	return listCostumeItemPhotos(ctx, db,
		`SELECT p.id, p.production_id, p.costume_item_id, p.original_name,
		        p.display_name, p.thumbnail_name, p.media_type, p.status,
		        p.error_message, p.created_at, p.updated_at
		 FROM costume_item_photos AS p
		 JOIN costume_items AS i
		   ON i.production_id = p.production_id AND i.id = p.costume_item_id
		 WHERE p.production_id = ? AND i.actor_id = ? AND p.status = 'ready'
		   AND p.id = (
		       SELECT candidate.id
		       FROM costume_item_photos AS candidate
		       WHERE candidate.production_id = p.production_id
		         AND candidate.costume_item_id = p.costume_item_id
		         AND candidate.status = 'ready'
		       ORDER BY candidate.created_at, candidate.id
		       LIMIT 1
		   )
		 ORDER BY p.costume_item_id`,
		[]any{productionID, actorID}, "list first ready costume item photos by actor",
	)
}

func (r *costumeItemPhotoRepository) ListPending(ctx context.Context, limit int) ([]CostumeItemPhoto, error) {
	if limit <= 0 {
		return nil, errors.New("storage: pending photo limit must be positive")
	}
	db, err := r.database()
	if err != nil {
		return nil, err
	}
	return listCostumeItemPhotos(ctx, db,
		`SELECT `+costumeItemPhotoColumns+`
		 FROM costume_item_photos
		 WHERE status = 'pending'
		 ORDER BY created_at, id
		 LIMIT ?`,
		[]any{limit}, "list pending costume item photos",
	)
}

func listCostumeItemPhotos(ctx context.Context, db *sql.DB, query string, args []any, operation string) ([]CostumeItemPhoto, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("storage: %s: %w", operation, err)
	}
	defer rows.Close()
	photos := make([]CostumeItemPhoto, 0)
	for rows.Next() {
		photo, err := scanCostumeItemPhoto(rows)
		if err != nil {
			return nil, fmt.Errorf("storage: scan costume item photo: %w", err)
		}
		photos = append(photos, photo)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: %s: %w", operation, err)
	}
	return photos, nil
}

func (r *costumeItemPhotoRepository) MarkReady(ctx context.Context, photoID int64) (CostumeItemPhoto, error) {
	return r.transition(ctx, photoID, PhotoStatusReady, "")
}

func (r *costumeItemPhotoRepository) MarkFailed(ctx context.Context, photoID int64, errorMessage string) (CostumeItemPhoto, error) {
	return r.transition(ctx, photoID, PhotoStatusFailed, errorMessage)
}

func (r *costumeItemPhotoRepository) transition(ctx context.Context, photoID int64, status, errorMessage string) (CostumeItemPhoto, error) {
	if err := validatePhotoStatus(status); err != nil {
		return CostumeItemPhoto{}, err
	}
	if status == PhotoStatusPending {
		return CostumeItemPhoto{}, fmt.Errorf("%w: cannot transition a photo to %q", ErrInvalidPhotoStatus, status)
	}
	db, err := r.database()
	if err != nil {
		return CostumeItemPhoto{}, err
	}
	photo, err := scanCostumeItemPhoto(db.QueryRowContext(ctx,
		`UPDATE costume_item_photos
		 SET status = ?, error_message = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		 WHERE id = ? AND status = ?
		 RETURNING `+costumeItemPhotoColumns,
		status, errorMessage, photoID, PhotoStatusPending,
	))
	if err == nil {
		return photo, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return CostumeItemPhoto{}, fmt.Errorf("storage: mark costume item photo %s: %w", status, err)
	}
	var currentStatus string
	if err := db.QueryRowContext(ctx, `SELECT status FROM costume_item_photos WHERE id = ?`, photoID).Scan(&currentStatus); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return CostumeItemPhoto{}, notFound("costume item photo")
		}
		return CostumeItemPhoto{}, fmt.Errorf("storage: inspect costume item photo status: %w", err)
	}
	return CostumeItemPhoto{}, fmt.Errorf("%w: cannot transition costume item photo %d from %q to %q", ErrInvalidPhotoStatus, photoID, currentStatus, status)
}
