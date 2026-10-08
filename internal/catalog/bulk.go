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

// MaxBulk is the most rows one bulk request takes (BR-043).
const MaxBulk = 500

// BulkItem is one row of POST /v1/products/bulk.
type BulkItem struct {
	SKU    *string
	Title  *string
	Status *string
	Fields VariantFields
}

// BulkRowResult is what happened to one row, by its request index.
type BulkRowResult struct {
	Index        int
	SKU          *string
	Status       string // created, updated, error
	VariantID    *uuid.UUID
	Code, Detail string
}

type BulkResult struct {
	Created, Updated, Failed int
	Results                  []BulkRowResult
}

// Bulk upserts rows keyed on SKU, each under its own savepoint, so one bad
// row fails alone and the rest apply (BR-043).
func (s *Service) Bulk(ctx context.Context, items []BulkItem, onConflict string) (BulkResult, error) {
	var out BulkResult
	err := s.tx(ctx, func(q *sqlcgen.Queries, tx pgx.Tx) error {
		for i, item := range items {
			res := s.bulkRow(ctx, q, tx, item, onConflict)
			res.Index, res.SKU = i, item.SKU
			switch res.Status {
			case "created":
				out.Created++
			case "updated":
				out.Updated++
			default:
				out.Failed++
			}
			out.Results = append(out.Results, res)
		}
		return db.Audit(ctx, tx, db.AuditEntry{Action: "product.bulk", SubjectType: "products", SubjectID: "bulk",
			After: map[string]any{"created": out.Created, "updated": out.Updated, "failed": out.Failed}})
	})
	return out, err
}

func (s *Service) bulkRow(ctx context.Context, q *sqlcgen.Queries, tx pgx.Tx, item BulkItem, onConflict string) BulkRowResult {
	if _, err := tx.Exec(ctx, "SAVEPOINT bulk_row"); err != nil {
		return rowError(err)
	}
	res, err := s.bulkApply(ctx, q, item, onConflict)
	if err != nil {
		_, _ = tx.Exec(ctx, "ROLLBACK TO SAVEPOINT bulk_row")
		return rowError(err)
	}
	_, _ = tx.Exec(ctx, "RELEASE SAVEPOINT bulk_row")
	return res
}

func (s *Service) bulkApply(ctx context.Context, q *sqlcgen.Queries, item BulkItem, onConflict string) (BulkRowResult, error) {
	if item.SKU != nil && strings.TrimSpace(*item.SKU) == "" {
		return BulkRowResult{}, fieldError("sku", "sku cannot be blank")
	}
	var existing *Variant
	if item.SKU != nil {
		v, err := q.VariantBySKU(ctx, *item.SKU)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return BulkRowResult{}, err
		}
		if err == nil {
			ev := Variant(v)
			existing = &ev
		}
	}

	if existing != nil {
		if onConflict == "error" {
			holder, _ := q.SKUHolder(ctx, *item.SKU)
			return BulkRowResult{}, apperrors.DuplicateSKU(*item.SKU, holder.Title)
		}
		if existing.ArchivedAt != nil {
			return BulkRowResult{}, fieldError("sku", "this SKU belongs to an archived variant")
		}
		if _, err := q.UpdateVariant(ctx, updateParams(existing.ID, int(existing.Version), item.Fields)); err != nil {
			return BulkRowResult{}, variantWriteError(err)
		}
		if err := s.bulkStatus(ctx, q, existing.ProductID, item.Status); err != nil {
			return BulkRowResult{}, err
		}
		return BulkRowResult{Status: "updated", VariantID: &existing.ID}, nil
	}

	if item.Title == nil || strings.TrimSpace(*item.Title) == "" {
		return BulkRowResult{}, fieldError("title", "title is required to create a product")
	}
	slug, err := freeSlug(ctx, q, *item.Title)
	if err != nil {
		return BulkRowResult{}, err
	}
	p, err := q.CreateProduct(ctx, sqlcgen.CreateProductParams{ID: uuid.Must(uuid.NewV7()),
		Title: strings.TrimSpace(*item.Title), Slug: slug, Attributes: []byte("{}")})
	if err != nil {
		return BulkRowResult{}, err
	}
	f := item.Fields
	f.SKU = item.SKU
	id, err := q.CreateVariant(ctx, createParams(uuid.Must(uuid.NewV7()), p.ID, nil, f))
	if err != nil {
		return BulkRowResult{}, variantWriteError(err)
	}
	if err := s.bulkStatus(ctx, q, p.ID, item.Status); err != nil {
		return BulkRowResult{}, err
	}
	return BulkRowResult{Status: "created", VariantID: &id}, nil
}

// bulkStatus applies a row's status to its product; active runs the publish
// check (BR-038).
func (s *Service) bulkStatus(ctx context.Context, q *sqlcgen.Queries, product uuid.UUID, status *string) error {
	if status == nil {
		return nil
	}
	if *status != "draft" && *status != "active" {
		return fieldError("status", "status is draft or active")
	}
	if err := q.SetProductStatus(ctx, sqlcgen.SetProductStatusParams{ID: product, Status: *status}); err != nil {
		return err
	}
	if *status == "active" {
		return s.publishCheck(ctx, q, product)
	}
	return nil
}

func rowError(err error) BulkRowResult {
	var e *apperrors.Error
	if !errors.As(err, &e) {
		e = apperrors.Internal(err)
	}
	return BulkRowResult{Status: "error", Code: e.Code, Detail: e.Detail}
}
