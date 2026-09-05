package storage

import (
	"context"
	"errors"
	"time"
)

const (
	StatusNotStarted = "Not Started"
	StatusInProgress = "In Progress"
	StatusBlocked    = "Blocked"
	StatusComplete   = "Complete"
)

var (
	ErrNotFound = errors.New("storage: not found")
	ErrArchived = errors.New("storage: record is archived")
)

type Production struct {
	ID         int64
	Name       string
	ArchivedAt *time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type CreateProductionInput struct {
	Name string
}

type UpdateProductionInput struct {
	ID   int64
	Name string
}

// ProductionRepository persists production records and their archive state.
type ProductionRepository interface {
	Create(context.Context, CreateProductionInput) (Production, error)
	Get(context.Context, int64) (Production, error)
	List(context.Context, ...bool) ([]Production, error)
	Update(context.Context, UpdateProductionInput) (Production, error)
	Archive(context.Context, int64) error
}

type Actor struct {
	ID           int64
	ProductionID int64
	Name         string
	Role         string
	Notes        string
	ArchivedAt   *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type CreateActorInput struct {
	ProductionID int64
	Name         string
	Role         string
	Notes        string
}

type UpdateActorInput struct {
	ProductionID int64
	ID           int64
	Name         string
	Role         string
	Notes        string
}

// ActorRepository always takes a production scope for actor lookups and writes.
type ActorRepository interface {
	Create(context.Context, CreateActorInput) (Actor, error)
	Get(context.Context, int64, int64) (Actor, error)
	List(context.Context, int64, ...bool) ([]Actor, error)
	Update(context.Context, UpdateActorInput) (Actor, error)
	Archive(context.Context, int64, int64) error
}

type ItemType struct {
	ID           int64
	ProductionID int64
	Name         string
	ArchivedAt   *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type CreateItemTypeInput struct {
	ProductionID int64
	Name         string
}

type UpdateItemTypeInput struct {
	ProductionID int64
	ID           int64
	Name         string
}

// ItemTypeRepository always takes a production scope for item-type lookups and writes.
type ItemTypeRepository interface {
	Create(context.Context, CreateItemTypeInput) (ItemType, error)
	Get(context.Context, int64, int64) (ItemType, error)
	List(context.Context, int64, ...bool) ([]ItemType, error)
	Update(context.Context, UpdateItemTypeInput) (ItemType, error)
	Archive(context.Context, int64, int64) error
}

type CostumeItem struct {
	ID           int64
	ProductionID int64
	ActorID      int64
	ItemTypeID   int64
	Code         string
	Description  string
	Status       string
	Progress     int
	NextAction   string
	Blocker      string
	Notes        string
	ArchivedAt   *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type CreateCostumeItemInput struct {
	ProductionID int64
	ActorID      int64
	ItemTypeID   int64
	Description  string
	Status       string
	Progress     int
	NextAction   string
	Blocker      string
	Notes        string
}

type UpdateCostumeItemInput struct {
	ProductionID int64
	ID           int64
	ActorID      int64
	ItemTypeID   int64
	Description  string
	Status       string
	Progress     int
	NextAction   string
	Blocker      string
	Notes        string
}

type CostumeItemFilter struct {
	ProductionID    int64
	ActorID         int64
	ItemTypeID      int64
	Code            string
	Status          string
	IncludeArchived bool
	Incomplete      bool
	Blocked         bool
}

// CostumeItemRepository allocates immutable production-scoped item codes and
// exposes filters that keep all reads within a production.
type CostumeItemRepository interface {
	Create(context.Context, CreateCostumeItemInput) (CostumeItem, error)
	Get(context.Context, int64, int64) (CostumeItem, error)
	List(context.Context, CostumeItemFilter) ([]CostumeItem, error)
	Update(context.Context, UpdateCostumeItemInput) (CostumeItem, error)
	Archive(context.Context, int64, int64) error
	AllocateCode(context.Context, int64) (string, error)
}
