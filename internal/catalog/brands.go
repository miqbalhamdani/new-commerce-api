package catalog

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/db/sqlcgen"
)

type Brand = sqlcgen.Brand

// BrandCursor is the keyset position after the last brand of a page.
type BrandCursor struct {
	Name string    `json:"n"`
	ID   uuid.UUID `json:"i"`
}

// CreateBrand derives the slug from the name (BR-030). A name whose slug is
// empty, or taken by any brand of this tenant including archived ones, is 422
// on name.
func (s *Service) CreateBrand(ctx context.Context, name string) (Brand, error) {
	var b Brand
	err := s.tx(ctx, func(q *sqlcgen.Queries, tx pgx.Tx) error {
		name, err := s.brandName(ctx, q, name)
		if err != nil {
			return err
		}
		b, err = q.CreateBrand(ctx, sqlcgen.CreateBrandParams{ID: uuid.Must(uuid.NewV7()), Name: name})
		if err != nil {
			return brandWriteError(err)
		}
		return db.Audit(ctx, tx, db.AuditEntry{Action: "brand.create", SubjectType: "brand",
			SubjectID: b.ID.String(), After: map[string]string{"name": b.Name}})
	})
	return b, err
}

// RenameBrand re-derives the slug. Brands have no version: last save wins
// (BR-010).
func (s *Service) RenameBrand(ctx context.Context, id uuid.UUID, name string) (Brand, error) {
	var b Brand
	err := s.tx(ctx, func(q *sqlcgen.Queries, tx pgx.Tx) error {
		before, err := q.GetBrand(ctx, id)
		if err != nil {
			return notFound(err, "brand")
		}
		if name, err = s.brandName(ctx, q, name); err != nil {
			return err
		}
		if b, err = q.RenameBrand(ctx, sqlcgen.RenameBrandParams{ID: id, Name: name}); err != nil {
			return brandWriteError(err)
		}
		return db.Audit(ctx, tx, db.AuditEntry{Action: "brand.update", SubjectType: "brand",
			SubjectID: id.String(), Before: map[string]string{"name": before.Name},
			After: map[string]string{"name": b.Name}})
	})
	return b, err
}

// ArchiveBrand sets archived_at (BR-012). Products keep their brand.
func (s *Service) ArchiveBrand(ctx context.Context, id uuid.UUID) error {
	return s.tx(ctx, func(q *sqlcgen.Queries, tx pgx.Tx) error {
		b, err := q.ArchiveBrand(ctx, id)
		if err != nil {
			return notFound(err, "brand")
		}
		return db.Audit(ctx, tx, db.AuditEntry{Action: "brand.archive", SubjectType: "brand",
			SubjectID: id.String(), After: map[string]any{"archived_at": b.ArchivedAt}})
	})
}

func (s *Service) GetBrand(ctx context.Context, id uuid.UUID) (Brand, error) {
	var b Brand
	err := s.tx(ctx, func(q *sqlcgen.Queries, _ pgx.Tx) (err error) {
		b, err = q.GetBrand(ctx, id)
		return notFound(err, "brand")
	})
	return b, err
}

// ListBrands returns up to limit brands after the cursor, and the cursor for
// the next page (nil on the last one).
func (s *Service) ListBrands(ctx context.Context, q *string, archived bool, after *BrandCursor, limit int) ([]Brand, *BrandCursor, error) {
	params := sqlcgen.ListBrandsParams{Archived: archived, Q: likePattern(q), Lim: int32(limit + 1)}
	if after != nil {
		params.AfterName, params.AfterID = &after.Name, &after.ID
	}
	var rows []Brand
	err := s.tx(ctx, func(qs *sqlcgen.Queries, _ pgx.Tx) (err error) {
		rows, err = qs.ListBrands(ctx, params)
		return err
	})
	if err != nil || len(rows) <= limit {
		return rows, nil, err
	}
	rows = rows[:limit]
	last := rows[limit-1]
	return rows, &BrandCursor{Name: last.Name, ID: last.ID}, nil
}

func (s *Service) brandName(ctx context.Context, q *sqlcgen.Queries, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fieldError("name", "name is required")
	}
	slug, err := q.Slugify(ctx, name)
	if err != nil {
		return "", err
	}
	if slug == "" {
		return "", fieldError("name", "name needs at least one letter or digit")
	}
	return name, nil
}

func brandWriteError(err error) error {
	if code, constraint, ok := db.Violation(err); ok && code == "23505" && constraint == "brands_tenant_id_slug_key" {
		return fieldError("name", "another brand already uses this name, or one that slugifies the same (archived brands included)")
	}
	return err
}
