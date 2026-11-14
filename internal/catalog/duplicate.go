package catalog

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/db/sqlcgen"
)

// DuplicateProduct is BR-047: a draft copy of any product, archived included,
// with " (copy)" after its title, a slug of its own, the same details and
// categories, its live variants without SKUs or barcodes, and its images,
// which share the original's files.
func (s *Service) DuplicateProduct(ctx context.Context, id uuid.UUID) (ProductView, error) {
	var copyID uuid.UUID
	err := s.tx(ctx, func(q *sqlcgen.Queries, tx pgx.Tx) error {
		src, err := q.GetProduct(ctx, id)
		if err != nil {
			return notFound(err, "product")
		}
		title := src.Title + " (copy)"
		slug, err := freeSlug(ctx, q, title)
		if err != nil {
			return err
		}
		p, err := q.CopyProduct(ctx, sqlcgen.CopyProductParams{ID: uuid.Must(uuid.NewV7()), Title: title, Slug: slug, Source: id})
		if err != nil {
			return err
		}
		copyID = p.ID
		if err := q.CopyProductCategories(ctx, sqlcgen.CopyProductCategoriesParams{Target: copyID, Source: id}); err != nil {
			return err
		}
		variants, err := q.ListVariants(ctx, sqlcgen.ListVariantsParams{ProductID: id, Archived: false})
		if err != nil {
			return err
		}
		twin := make(map[uuid.UUID]uuid.UUID, len(variants)) // the original's live variant -> its copy
		for _, v := range variants {
			f := VariantFields{RegularPrice: &v.RegularPriceAmount, SalePrice: v.SalePriceAmount,
				SaleStartsAt: v.SaleStartsAt, SaleEndsAt: v.SaleEndsAt, WeightGrams: &v.WeightGrams, PriceUSD: v.PriceUsd}
			if twin[v.ID], err = q.CreateVariant(ctx, createParams(uuid.Must(uuid.NewV7()), copyID, v.OptionValues, f)); err != nil {
				return variantWriteError(err)
			}
		}
		media, err := q.ListProductMedia(ctx, id)
		if err != nil {
			return err
		}
		for _, m := range media {
			var variant *uuid.UUID
			if m.VariantID != nil {
				if c, ok := twin[*m.VariantID]; ok {
					variant = &c
				}
			}
			if err := q.CopyMedia(ctx, sqlcgen.CopyMediaParams{ID: uuid.Must(uuid.NewV7()), ProductID: copyID,
				VariantID: variant, Source: m.ID}); err != nil {
				return err
			}
		}
		return db.Audit(ctx, tx, db.AuditEntry{Action: "product.create", SubjectType: "product", SubjectID: copyID.String(),
			After: map[string]any{"title": p.Title, "slug": p.Slug, "status": p.Status, "duplicate_of": id}})
	})
	if err != nil {
		return ProductView{}, err
	}
	return s.GetProduct(ctx, copyID)
}
