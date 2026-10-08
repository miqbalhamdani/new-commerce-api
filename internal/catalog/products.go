package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/db/sqlcgen"
	apperrors "github.com/miqbalhamdani/new-commerce-api/internal/platform/errors"
)

// ProductView is a product with its references expanded for a response.
type ProductView struct {
	sqlcgen.Product
	Brand        *Brand
	Categories   []sqlcgen.ProductCategoriesRow
	VariantCount int
	Media        []Media
}

// ProductInput is a create.
type ProductInput struct {
	Title       string
	Description *string
	BrandID     *uuid.UUID
	CategoryIDs []uuid.UUID
	Attributes  map[string]any
}

// ProductPatch is a PATCH: nil leaves a field alone; the Clear flags set a
// nullable column to NULL (BR-009).
type ProductPatch struct {
	Title            *string
	Slug             *string
	Description      *string
	ClearDescription bool
	BrandID          *uuid.UUID
	ClearBrand       bool
	CategoryIDs      *[]uuid.UUID
	Attributes       map[string]any
	Status           *string
}

var slugPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// CreateProduct makes a draft (BR-037) with a slug derived from the title,
// suffixed -2, -3, … past any product that holds it, archived ones included
// (BR-042).
func (s *Service) CreateProduct(ctx context.Context, in ProductInput) (ProductView, error) {
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" {
		return ProductView{}, fieldError("title", "title is required")
	}
	var id uuid.UUID
	err := s.tx(ctx, func(q *sqlcgen.Queries, tx pgx.Tx) error {
		if err := checkRefs(ctx, q, in.BrandID, in.CategoryIDs); err != nil {
			return err
		}
		slug, err := freeSlug(ctx, q, in.Title)
		if err != nil {
			return err
		}
		attrs, err := json.Marshal(orEmpty(in.Attributes))
		if err != nil {
			return err
		}
		p, err := q.CreateProduct(ctx, sqlcgen.CreateProductParams{ID: uuid.Must(uuid.NewV7()), Title: in.Title,
			Slug: slug, Description: in.Description, BrandID: in.BrandID, Attributes: attrs})
		if err != nil {
			return err
		}
		id = p.ID
		if err := setCategories(ctx, q, id, in.CategoryIDs); err != nil {
			return err
		}
		return db.Audit(ctx, tx, db.AuditEntry{Action: "product.create", SubjectType: "product",
			SubjectID: id.String(), After: map[string]any{"title": p.Title, "slug": p.Slug, "status": p.Status}})
	})
	if err != nil {
		return ProductView{}, err
	}
	return s.GetProduct(ctx, id)
}

// UpdateProduct applies a PATCH at the version the client read (BR-010).
func (s *Service) UpdateProduct(ctx context.Context, id uuid.UUID, version int, p ProductPatch) (ProductView, error) {
	if p.Title != nil {
		t := strings.TrimSpace(*p.Title)
		if t == "" {
			return ProductView{}, fieldError("title", "title cannot be empty")
		}
		p.Title = &t
	}
	if p.Slug != nil && !slugPattern.MatchString(*p.Slug) {
		return ProductView{}, fieldError("slug", "slug is lower-case letters and digits joined by single hyphens")
	}
	err := s.tx(ctx, func(q *sqlcgen.Queries, tx pgx.Tx) error {
		before, err := q.GetProduct(ctx, id)
		if err != nil {
			return notFound(err, "product")
		}
		if before.ArchivedAt != nil {
			return fieldError("status", "an archived product cannot be edited")
		}
		if int(before.Version) != version {
			return apperrors.VersionConflict(int(before.Version), version)
		}
		var cats []uuid.UUID
		if p.CategoryIDs != nil {
			cats = *p.CategoryIDs
		}
		if err := checkRefs(ctx, q, p.BrandID, cats); err != nil {
			return err
		}
		if p.Slug != nil {
			taken, err := q.ProductSlugTaken(ctx, sqlcgen.ProductSlugTakenParams{Slug: *p.Slug, ExceptID: &id})
			if err != nil {
				return err
			}
			if taken {
				return fieldError("slug", "another product already uses this slug (archived products included)")
			}
		}
		params := sqlcgen.UpdateProductParams{ID: id, Version: int32(version), Title: p.Title, Slug: p.Slug,
			SetDescription: p.Description != nil || p.ClearDescription, Description: p.Description,
			SetBrand: p.BrandID != nil || p.ClearBrand, BrandID: p.BrandID, Status: p.Status}
		if p.Attributes != nil {
			if params.Attributes, err = json.Marshal(p.Attributes); err != nil {
				return err
			}
		}
		after, err := q.UpdateProduct(ctx, params)
		if errors.Is(err, pgx.ErrNoRows) { // the version moved between the read and the write
			return apperrors.VersionConflict(int(before.Version)+1, version)
		}
		if err != nil {
			return err
		}
		if p.CategoryIDs != nil {
			if err := q.ClearProductCategories(ctx, id); err != nil {
				return err
			}
			if err := setCategories(ctx, q, id, cats); err != nil {
				return err
			}
		}
		if p.Status != nil && *p.Status == "active" && before.Status != "active" {
			if err := s.publishCheck(ctx, q, id); err != nil {
				return err
			}
		}
		return db.Audit(ctx, tx, db.AuditEntry{Action: "product.update", SubjectType: "product", SubjectID: id.String(),
			Before: productAudit(before), After: productAudit(after)})
	})
	if err != nil {
		return ProductView{}, err
	}
	return s.GetProduct(ctx, id)
}

// ArchiveProduct sets status archived and archived_at (BR-012); orders keep
// their lines (BR-045).
func (s *Service) ArchiveProduct(ctx context.Context, id uuid.UUID) error {
	return s.tx(ctx, func(q *sqlcgen.Queries, tx pgx.Tx) error {
		p, err := q.ArchiveProduct(ctx, id)
		if err != nil {
			return notFound(err, "product")
		}
		return db.Audit(ctx, tx, db.AuditEntry{Action: "product.archive", SubjectType: "product",
			SubjectID: id.String(), After: map[string]any{"status": p.Status}})
	})
}

func (s *Service) GetProduct(ctx context.Context, id uuid.UUID) (ProductView, error) {
	var v ProductView
	err := s.tx(ctx, func(q *sqlcgen.Queries, _ pgx.Tx) error {
		p, err := q.GetProduct(ctx, id)
		if err != nil {
			return notFound(err, "product")
		}
		v.Product = p
		if p.BrandID != nil {
			b, err := q.GetBrand(ctx, *p.BrandID)
			if err != nil {
				return err
			}
			v.Brand = &b
		}
		if v.Categories, err = q.ProductCategories(ctx, id); err != nil {
			return err
		}
		n, err := q.LiveVariantCount(ctx, id)
		v.VariantCount = int(n)
		if err != nil {
			return err
		}
		v.Media, err = productMedia(ctx, q, id)
		return err
	})
	return v, err
}

// checkRefs requires the brand and every category to be live rows of this
// tenant; RLS hides other tenants' rows, so they read as missing.
func checkRefs(ctx context.Context, q *sqlcgen.Queries, brand *uuid.UUID, categories []uuid.UUID) error {
	if brand != nil {
		b, err := q.GetBrand(ctx, *brand)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && b.ArchivedAt != nil) {
			return fieldError("brand_id", "no such live brand")
		}
		if err != nil {
			return err
		}
	}
	if len(categories) == 0 {
		return nil
	}
	ids := slices.Clone(categories)
	slices.SortFunc(ids, func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) })
	ids = slices.Compact(ids)
	n, err := q.LiveCategoryCount(ctx, ids)
	if err != nil {
		return err
	}
	if int(n) != len(ids) {
		return fieldError("category_ids", "every category must be a live category of this shop")
	}
	return nil
}

func setCategories(ctx context.Context, q *sqlcgen.Queries, product uuid.UUID, categories []uuid.UUID) error {
	if len(categories) == 0 {
		return nil
	}
	ids := slices.Clone(categories)
	slices.SortFunc(ids, func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) })
	return q.AddProductCategories(ctx, sqlcgen.AddProductCategoriesParams{ProductID: product, CategoryIds: slices.Compact(ids)})
}

func freeSlug(ctx context.Context, q *sqlcgen.Queries, title string) (string, error) {
	base, err := q.Slugify(ctx, title)
	if err != nil {
		return "", err
	}
	if base == "" {
		base = "product"
	}
	for n := 1; ; n++ {
		slug := base
		if n > 1 {
			slug = base + "-" + strconv.Itoa(n)
		}
		taken, err := q.ProductSlugTaken(ctx, sqlcgen.ProductSlugTakenParams{Slug: slug})
		if err != nil || !taken {
			return slug, err
		}
	}
}

// publishCheck is BR-038, run when a product goes active.
//
// ponytail: a no-op until P1-049, which needs variants (P1-029/040) and media
// (P1-043) to have anything to check.
func (s *Service) publishCheck(_ context.Context, _ *sqlcgen.Queries, _ uuid.UUID) error {
	return nil
}

func productAudit(p sqlcgen.Product) map[string]any {
	return map[string]any{"title": p.Title, "slug": p.Slug, "status": p.Status,
		"description": p.Description, "brand_id": p.BrandID, "version": p.Version}
}

func orEmpty(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}
