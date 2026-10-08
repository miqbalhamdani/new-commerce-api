package catalog

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/db/sqlcgen"
	apperrors "github.com/miqbalhamdani/new-commerce-api/internal/platform/errors"
)

type Category = sqlcgen.Category

// CategoryDetail is a category with the counts the move dialog states (BR-033).
type CategoryDetail struct {
	Category
	DescendantCount int
	ProductCount    int
}

// CategoryUpdate is a PATCH: a nil field is left alone. ClearParent makes the
// category a root.
type CategoryUpdate struct {
	Name        *string
	Parent      *uuid.UUID
	ClearParent bool
}

func (s *Service) CreateCategory(ctx context.Context, name, kind string, parent *uuid.UUID) (Category, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Category{}, fieldError("name", "name is required")
	}
	var c Category
	err := s.tx(ctx, func(q *sqlcgen.Queries, tx pgx.Tx) error {
		if err := q.LockCategories(ctx); err != nil {
			return err
		}
		if err := checkParent(ctx, q, parent, kind); err != nil {
			return err
		}
		var err error
		c, err = q.CreateCategory(ctx, sqlcgen.CreateCategoryParams{
			ID: uuid.Must(uuid.NewV7()), ParentID: parent, Kind: kind, Name: name})
		if err != nil {
			return categoryWriteError(err)
		}
		return db.Audit(ctx, tx, db.AuditEntry{Action: "category.create", SubjectType: "category",
			SubjectID: c.ID.String(), After: map[string]any{"name": c.Name, "kind": c.Kind, "parent_id": c.ParentID}})
	})
	return c, err
}

// UpdateCategory renames or moves. The triggers rewrite the subtree's paths
// in the same statement; product links are untouched (BR-032, BR-033).
func (s *Service) UpdateCategory(ctx context.Context, id uuid.UUID, u CategoryUpdate) (Category, error) {
	if u.Name != nil {
		trimmed := strings.TrimSpace(*u.Name)
		if trimmed == "" {
			return Category{}, fieldError("name", "name is required")
		}
		u.Name = &trimmed
	}
	var c Category
	err := s.tx(ctx, func(q *sqlcgen.Queries, tx pgx.Tx) error {
		if err := q.LockCategories(ctx); err != nil {
			return err
		}
		before, err := q.GetCategory(ctx, id)
		if err != nil {
			return notFound(err, "category")
		}
		if u.Parent != nil {
			if err := checkParent(ctx, q, u.Parent, before.Kind); err != nil {
				return err
			}
		}
		params := sqlcgen.UpdateCategoryParams{ID: id, SetName: u.Name != nil, SetParent: u.Parent != nil || u.ClearParent, ParentID: u.Parent}
		if u.Name != nil {
			params.Name = *u.Name
		}
		if c, err = q.UpdateCategory(ctx, params); err != nil {
			return categoryWriteError(err)
		}
		return db.Audit(ctx, tx, db.AuditEntry{Action: "category.update", SubjectType: "category", SubjectID: id.String(),
			Before: map[string]any{"name": before.Name, "parent_id": before.ParentID, "path": before.Path},
			After:  map[string]any{"name": c.Name, "parent_id": c.ParentID, "path": c.Path}})
	})
	return c, err
}

// ArchiveCategory refuses a category with live children or live products in
// its subtree, naming both counts (BR-036).
func (s *Service) ArchiveCategory(ctx context.Context, id uuid.UUID) error {
	return s.tx(ctx, func(q *sqlcgen.Queries, tx pgx.Tx) error {
		if err := q.LockCategories(ctx); err != nil {
			return err
		}
		if _, err := q.GetCategory(ctx, id); err != nil {
			return notFound(err, "category")
		}
		counts, err := q.CategoryCounts(ctx, id)
		if err != nil {
			return err
		}
		if counts.DescendantCount > 0 || counts.ProductCount > 0 {
			return apperrors.CategoryInUse(counts.DescendantCount, counts.ProductCount)
		}
		if err := q.ArchiveCategory(ctx, id); err != nil {
			return err
		}
		return db.Audit(ctx, tx, db.AuditEntry{Action: "category.archive", SubjectType: "category", SubjectID: id.String()})
	})
}

func (s *Service) GetCategory(ctx context.Context, id uuid.UUID) (CategoryDetail, error) {
	var d CategoryDetail
	err := s.tx(ctx, func(q *sqlcgen.Queries, _ pgx.Tx) error {
		c, err := q.GetCategory(ctx, id)
		if err != nil {
			return notFound(err, "category")
		}
		counts, err := q.CategoryCounts(ctx, id)
		d = CategoryDetail{Category: c, DescendantCount: int(counts.DescendantCount), ProductCount: int(counts.ProductCount)}
		return err
	})
	return d, err
}

func (s *Service) ListCategories(ctx context.Context, kind *string, parent *uuid.UUID, depth *int) ([]Category, error) {
	params := sqlcgen.ListCategoriesParams{Kind: kind, ParentID: parent}
	if depth != nil {
		d := int32(*depth)
		params.Depth = &d
	}
	var rows []Category
	err := s.tx(ctx, func(q *sqlcgen.Queries, _ pgx.Tx) (err error) {
		if parent != nil {
			if _, err := q.GetCategory(ctx, *parent); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return fieldError("parent_id", "no such category")
				}
				return err
			}
		}
		rows, err = q.ListCategories(ctx, params)
		return err
	})
	return rows, err
}

// checkParent requires a live category of the same kind in this tenant. The
// trigger would refuse the others too, but with an error no client can act on.
func checkParent(ctx context.Context, q *sqlcgen.Queries, parent *uuid.UUID, kind string) error {
	if parent == nil {
		return nil
	}
	p, err := q.GetCategory(ctx, *parent)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return fieldError("parent_id", "no such category")
	case err != nil:
		return err
	case p.ArchivedAt != nil:
		return fieldError("parent_id", "the parent is archived")
	case p.Kind != kind:
		return fieldError("parent_id", "the parent is in a "+p.Kind+" tree, not "+kind)
	}
	return nil
}

func categoryWriteError(err error) error {
	if code, _, ok := db.Violation(err); ok && code == "P0001" && strings.Contains(err.Error(), "beneath its own descendant") {
		return fieldError("parent_id", "a category cannot move beneath itself or one of its descendants")
	}
	return err
}
