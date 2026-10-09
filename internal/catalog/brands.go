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

// CreateBrand uses slug, or derives it from the name when slug is nil
// (BR-030). A slug that is taken by any brand of this tenant, deleted ones
// included, is 422 on slug when sent, else on name.
func (s *Service) CreateBrand(ctx context.Context, name string, slug *string) (Brand, error) {
	var b Brand
	err := s.tx(ctx, func(q *sqlcgen.Queries, tx pgx.Tx) error {
		name, err := s.brandName(ctx, q, name, slug)
		if err != nil {
			return err
		}
		b, err = q.CreateBrand(ctx, sqlcgen.CreateBrandParams{ID: uuid.Must(uuid.NewV7()), Name: name, Slug: slug})
		if err != nil {
			return brandWriteError(err, slug)
		}
		return db.Audit(ctx, tx, db.AuditEntry{Action: "brand.create", SubjectType: "brand",
			SubjectID: b.ID.String(), After: map[string]string{"name": b.Name, "slug": b.Slug}})
	})
	return b, err
}

// RenameBrand sets the name and slug. A nil name keeps the current one; a nil
// slug is re-derived from the name. Brands have no version: last save wins
// (BR-010).
func (s *Service) RenameBrand(ctx context.Context, id uuid.UUID, newName, slug *string) (Brand, error) {
	var b Brand
	err := s.tx(ctx, func(q *sqlcgen.Queries, tx pgx.Tx) error {
		before, err := q.GetBrand(ctx, id)
		if err != nil {
			return notFound(err, "brand")
		}
		name := before.Name
		if newName != nil {
			name = *newName
		}
		if name, err = s.brandName(ctx, q, name, slug); err != nil {
			return err
		}
		if b, err = q.UpdateBrand(ctx, sqlcgen.UpdateBrandParams{ID: id, Name: name, Slug: slug}); err != nil {
			return brandWriteError(err, slug)
		}
		return db.Audit(ctx, tx, db.AuditEntry{Action: "brand.update", SubjectType: "brand",
			SubjectID: id.String(), Before: map[string]string{"name": before.Name, "slug": before.Slug},
			After: map[string]string{"name": b.Name, "slug": b.Slug}})
	})
	return b, err
}

// ArchiveBrand is the brand's soft delete: it sets archived_at and takes the
// brand off every product (BR-012). Products never block it.
func (s *Service) ArchiveBrand(ctx context.Context, id uuid.UUID) error {
	return s.tx(ctx, func(q *sqlcgen.Queries, tx pgx.Tx) error {
		b, err := q.ArchiveBrand(ctx, id)
		if err != nil {
			return notFound(err, "brand")
		}
		cleared, err := q.ClearProductBrand(ctx, &id)
		if err != nil {
			return err
		}
		return db.Audit(ctx, tx, db.AuditEntry{Action: "brand.archive", SubjectType: "brand",
			SubjectID: id.String(), After: map[string]any{"archived_at": b.ArchivedAt, "products_cleared": cleared}})
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

// brandName trims and checks the name, and checks slug when the client sent
// one; otherwise the name must slugify to something.
func (s *Service) brandName(ctx context.Context, q *sqlcgen.Queries, name string, slug *string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fieldError("name", "name is required")
	}
	if slug != nil {
		if !slugPattern.MatchString(*slug) {
			return "", fieldError("slug", "slug is lower-case letters and digits joined by single hyphens")
		}
		return name, nil
	}
	derived, err := q.Slugify(ctx, name)
	if err != nil {
		return "", err
	}
	if derived == "" {
		return "", fieldError("name", "name needs at least one letter or digit")
	}
	return name, nil
}

func brandWriteError(err error, slug *string) error {
	if code, constraint, ok := db.Violation(err); ok && code == "23505" && constraint == "brands_tenant_id_slug_key" {
		if slug != nil {
			return fieldError("slug", "another brand already uses this slug (deleted brands included)")
		}
		return fieldError("name", "another brand already uses this name, or one that slugifies the same (deleted brands included); set a different slug")
	}
	return err
}
