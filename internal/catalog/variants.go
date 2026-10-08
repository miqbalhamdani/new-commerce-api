package catalog

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/db/sqlcgen"
	apperrors "github.com/miqbalhamdani/new-commerce-api/internal/platform/errors"
)

// Variant is a variant with its price as variant_price() decides it (BR-046).
type Variant = sqlcgen.GetVariantRow

// OnSale is true while the sale's schedule is running.
func OnSale(v Variant) bool { return v.Price < v.RegularPriceAmount }

// VariantFields are the writable columns of one variant, shared by create,
// PATCH, the matrix and bulk upsert. A nil pointer is "not sent"; the Clear
// flags are an explicit null (BR-009).
type VariantFields struct {
	SKU, Barcode               *string
	ClearSKU, ClearBarcode     bool
	RegularPrice               *int64
	SalePrice                  *int64
	ClearSalePrice             bool
	SaleStartsAt, SaleEndsAt   *time.Time
	ClearSaleStarts, ClearSale bool // ClearSale ends the schedule's end
	WeightGrams                *int32
}

func (s *Service) CreateVariant(ctx context.Context, productID uuid.UUID, options []string, f VariantFields) (Variant, error) {
	var v Variant
	err := s.tx(ctx, func(q *sqlcgen.Queries, tx pgx.Tx) error {
		p, err := q.GetProduct(ctx, productID)
		if err != nil {
			return notFound(err, "product")
		}
		if p.ArchivedAt != nil {
			return fieldError("product_id", "the product is archived")
		}
		if len(options) != len(p.OptionNames) {
			return fieldError("option_values", "option_values needs one value per option name; change the options through the variant matrix")
		}
		if err := checkSKU(ctx, q, f.SKU, uuid.Nil); err != nil {
			return err
		}
		id, err := q.CreateVariant(ctx, createParams(uuid.Must(uuid.NewV7()), productID, options, f))
		if err != nil {
			return variantWriteError(err)
		}
		if v, err = q.GetVariant(ctx, id); err != nil {
			return err
		}
		return db.Audit(ctx, tx, db.AuditEntry{Action: "variant.create", SubjectType: "variant",
			SubjectID: id.String(), After: variantAudit(v)})
	})
	return v, err
}

func (s *Service) UpdateVariant(ctx context.Context, id uuid.UUID, version int, f VariantFields) (Variant, error) {
	var v Variant
	err := s.tx(ctx, func(q *sqlcgen.Queries, tx pgx.Tx) error {
		before, err := q.GetVariant(ctx, id)
		if err != nil {
			return notFound(err, "variant")
		}
		if before.ArchivedAt != nil {
			return fieldError("archived_at", "an archived variant cannot be edited")
		}
		if int(before.Version) != version {
			return apperrors.VersionConflict(int(before.Version), version)
		}
		if err := checkSKU(ctx, q, f.SKU, id); err != nil {
			return err
		}
		_, err = q.UpdateVariant(ctx, updateParams(id, version, f))
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.VersionConflict(int(before.Version)+1, version)
		}
		if err != nil {
			return variantWriteError(err)
		}
		if v, err = q.GetVariant(ctx, id); err != nil {
			return err
		}
		return db.Audit(ctx, tx, db.AuditEntry{Action: "variant.update", SubjectType: "variant",
			SubjectID: id.String(), Before: variantAudit(before), After: variantAudit(v)})
	})
	return v, err
}

// ArchiveVariant archives (BR-012); orders keep their lines (BR-045).
func (s *Service) ArchiveVariant(ctx context.Context, id uuid.UUID) error {
	return s.tx(ctx, func(q *sqlcgen.Queries, tx pgx.Tx) error {
		if _, err := q.ArchiveVariant(ctx, id); err != nil {
			return notFound(err, "variant")
		}
		return db.Audit(ctx, tx, db.AuditEntry{Action: "variant.archive", SubjectType: "variant", SubjectID: id.String()})
	})
}

func (s *Service) ListVariants(ctx context.Context, productID uuid.UUID, archived bool) ([]Variant, error) {
	var out []Variant
	err := s.tx(ctx, func(q *sqlcgen.Queries, _ pgx.Tx) error {
		if _, err := q.GetProduct(ctx, productID); err != nil {
			return notFound(err, "product")
		}
		rows, err := q.ListVariants(ctx, sqlcgen.ListVariantsParams{ProductID: productID, Archived: archived})
		for _, r := range rows {
			out = append(out, Variant(r))
		}
		return err
	})
	return out, err
}

// checkSKU names the product already holding sku, so the 409 can say which
// (BR-039). The unique index still decides under a race; this is for the
// message. An empty SKU is refused: absent is null, not "".
func checkSKU(ctx context.Context, q *sqlcgen.Queries, sku *string, self uuid.UUID) error {
	if sku == nil {
		return nil
	}
	if strings.TrimSpace(*sku) == "" {
		return fieldError("sku", "sku cannot be blank; send null to clear it")
	}
	holder, err := q.SKUHolder(ctx, *sku)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && holder.ID == self) {
		return nil
	}
	if err != nil {
		return err
	}
	return apperrors.DuplicateSKU(*sku, holder.Title)
}

// variantWriteError maps the variant table's constraints to client errors.
func variantWriteError(err error) error {
	code, constraint, ok := db.Violation(err)
	if !ok {
		return err
	}
	switch {
	case code == "23505" && constraint == "variants_tenant_sku_uq":
		return apperrors.DuplicateSKU("", "")
	case code == "23505" && constraint == "variants_product_options_uq":
		return fieldError("option_values", "another live variant of this product has these option values")
	case code == "23514" && constraint == "variants_sale_below_regular":
		return fieldError("sale_price", "sale_price must be lower than regular_price")
	case code == "23514" && constraint == "variants_sale_window":
		return fieldError("sale_ends_at", "the sale must end after it starts")
	case code == "23514" && strings.Contains(constraint, "regular_price"):
		return fieldError("regular_price", "regular_price cannot be negative")
	case code == "23514" && strings.Contains(constraint, "weight"):
		return fieldError("weight_grams", "weight_grams cannot be negative")
	}
	return err
}

func createParams(id, product uuid.UUID, options []string, f VariantFields) sqlcgen.CreateVariantParams {
	p := sqlcgen.CreateVariantParams{ID: id, ProductID: product, OptionValues: options, Sku: f.SKU, Barcode: f.Barcode,
		SalePrice: f.SalePrice, SaleStartsAt: f.SaleStartsAt, SaleEndsAt: f.SaleEndsAt}
	if p.OptionValues == nil {
		p.OptionValues = []string{}
	}
	if f.RegularPrice != nil {
		p.RegularPrice = *f.RegularPrice
	}
	if f.WeightGrams != nil {
		p.WeightGrams = *f.WeightGrams
	}
	return p
}

func updateParams(id uuid.UUID, version int, f VariantFields) sqlcgen.UpdateVariantParams {
	return sqlcgen.UpdateVariantParams{ID: id, Version: int32(version),
		SetSku: f.SKU != nil || f.ClearSKU, Sku: f.SKU,
		SetBarcode: f.Barcode != nil || f.ClearBarcode, Barcode: f.Barcode,
		RegularPrice: f.RegularPrice,
		SetSalePrice: f.SalePrice != nil || f.ClearSalePrice, SalePrice: f.SalePrice,
		SetSaleStarts: f.SaleStartsAt != nil || f.ClearSaleStarts, SaleStartsAt: f.SaleStartsAt,
		SetSaleEnds: f.SaleEndsAt != nil || f.ClearSale, SaleEndsAt: f.SaleEndsAt,
		WeightGrams: f.WeightGrams}
}

func variantAudit(v Variant) map[string]any {
	return map[string]any{"sku": v.Sku, "option_values": v.OptionValues, "regular_price": v.RegularPriceAmount,
		"sale_price": v.SalePriceAmount, "sale_starts_at": v.SaleStartsAt, "sale_ends_at": v.SaleEndsAt,
		"weight_grams": v.WeightGrams, "version": v.Version}
}
