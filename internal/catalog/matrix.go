package catalog

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/db/sqlcgen"
	apperrors "github.com/miqbalhamdani/new-commerce-api/internal/platform/errors"
)

// MatrixRow is one row of the grid; Fields follow PATCH rules (omitted is
// unchanged, Clear is null).
type MatrixRow struct {
	OptionValues []string
	Fields       VariantFields
}

// MatrixRowResult is what happened to one row.
type MatrixRowResult struct {
	OptionValues []string
	Status       string // created, updated, restored, unchanged, error
	VariantID    *uuid.UUID
	Code, Detail string
}

// MatrixResult is the whole save.
type MatrixResult struct {
	ProductVersion                                          int
	Created, Updated, Restored, Unchanged, Archived, Failed int
	ArchivedIDs                                             []uuid.UUID
	Results                                                 []MatrixRowResult
}

// SaveMatrix applies the desired grid in one transaction (BR-041). Request
// errors -- a row that does not fit option_names, a duplicate row, Colour
// not first -- refuse everything before a write (BR-040). Otherwise each row
// runs under its own savepoint, so a bad row fails alone and the rest save.
func (s *Service) SaveMatrix(ctx context.Context, productID uuid.UUID, version int, names []string, rows []MatrixRow, archiveMissing bool) (MatrixResult, error) {
	if err := checkGrid(names, rows); err != nil {
		return MatrixResult{}, err
	}
	var out MatrixResult
	err := s.tx(ctx, func(q *sqlcgen.Queries, tx pgx.Tx) error {
		p, err := q.LockProduct(ctx, productID)
		if err != nil {
			return notFound(err, "product")
		}
		if p.ArchivedAt != nil {
			return fieldError("product_id", "the product is archived")
		}
		if int(p.Version) != version {
			return apperrors.VersionConflict(int(p.Version), version)
		}
		existing, err := q.AllVariants(ctx, productID)
		if err != nil {
			return err
		}
		hasLive := slices.ContainsFunc(existing, func(v sqlcgen.AllVariantsRow) bool { return v.ArchivedAt == nil })
		if !slices.Equal(p.OptionNames, names) && !archiveMissing && hasLive {
			// Live variants keyed on the old axes would no longer fit.
			return fieldError("archive_missing", "changing option_names needs archive_missing: true")
		}
		live, archived := map[string]Variant{}, map[string]Variant{}
		for _, v := range existing {
			if v.ArchivedAt == nil {
				live[gridKey(v.OptionValues)] = Variant(v)
			} else if _, seen := archived[gridKey(v.OptionValues)]; !seen {
				archived[gridKey(v.OptionValues)] = Variant(v) // the oldest archived one is restored
			}
		}

		v, err := q.BumpProduct(ctx, sqlcgen.BumpProductParams{ID: productID, OptionNames: orNone(names)})
		if err != nil {
			return err
		}
		out.ProductVersion = int(v)

		sent := map[string]bool{}
		for _, row := range rows {
			key := gridKey(row.OptionValues)
			sent[key] = true
			res := s.matrixRow(ctx, q, tx, productID, row, live[key], archived[key])
			switch res.Status {
			case "created":
				out.Created++
			case "updated":
				out.Updated++
			case "restored":
				out.Restored++
			case "unchanged":
				out.Unchanged++
			default:
				out.Failed++
			}
			out.Results = append(out.Results, res)
		}

		if archiveMissing {
			for key, v := range live {
				if sent[key] {
					continue
				}
				if _, err := q.ArchiveVariant(ctx, v.ID); err != nil {
					return err
				}
				out.Archived++
				out.ArchivedIDs = append(out.ArchivedIDs, v.ID)
			}
		}
		return db.Audit(ctx, tx, db.AuditEntry{Action: "product.variant_matrix", SubjectType: "product",
			SubjectID: productID.String(), Before: map[string]any{"option_names": p.OptionNames, "version": p.Version},
			After: map[string]any{"option_names": names, "version": out.ProductVersion, "created": out.Created,
				"updated": out.Updated, "restored": out.Restored, "archived": out.Archived, "failed": out.Failed}})
	})
	return out, err
}

// matrixRow saves one row under a savepoint and reports what happened.
func (s *Service) matrixRow(ctx context.Context, q *sqlcgen.Queries, tx pgx.Tx, productID uuid.UUID, row MatrixRow, live, archived Variant) MatrixRowResult {
	res := MatrixRowResult{OptionValues: row.OptionValues}
	fail := func(err error) MatrixRowResult {
		_, _ = tx.Exec(ctx, "ROLLBACK TO SAVEPOINT matrix_row")
		var e *apperrors.Error
		if !errors.As(variantWriteError(err), &e) {
			e = apperrors.Internal(err)
		}
		res.Status, res.VariantID, res.Code, res.Detail = "error", nil, e.Code, e.Detail
		return res
	}
	if _, err := tx.Exec(ctx, "SAVEPOINT matrix_row"); err != nil {
		return fail(err)
	}

	target := live
	switch {
	case live.ID != uuid.Nil:
		res.Status = "updated"
	case archived.ID != uuid.Nil:
		if err := q.RestoreVariant(ctx, archived.ID); err != nil {
			return fail(err)
		}
		target, res.Status = archived, "restored"
		target.Version++
	default:
		if err := checkSKU(ctx, q, row.Fields.SKU, uuid.Nil); err != nil {
			return fail(err)
		}
		id, err := q.CreateVariant(ctx, createParams(uuid.Must(uuid.NewV7()), productID, row.OptionValues, row.Fields))
		if err != nil {
			return fail(err)
		}
		res.Status, res.VariantID = "created", &id
		_, _ = tx.Exec(ctx, "RELEASE SAVEPOINT matrix_row")
		return res
	}

	res.VariantID = &target.ID
	if res.Status == "updated" && !changes(target, row.Fields) {
		res.Status = "unchanged"
		_, _ = tx.Exec(ctx, "RELEASE SAVEPOINT matrix_row")
		return res
	}
	if err := checkSKU(ctx, q, row.Fields.SKU, target.ID); err != nil {
		return fail(err)
	}
	if _, err := q.UpdateVariant(ctx, updateParams(target.ID, int(target.Version), row.Fields)); err != nil {
		return fail(err)
	}
	_, _ = tx.Exec(ctx, "RELEASE SAVEPOINT matrix_row")
	return res
}

// checkGrid is the request-level validation (BR-040): every row fits the
// axes, no two rows share option values, and Colour leads when present.
func checkGrid(names []string, rows []MatrixRow) error {
	seen := map[string]bool{}
	for i, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			return fieldError("option_names", "option names cannot be blank")
		}
		if seen[strings.ToLower(n)] {
			return fieldError("option_names", "option names must be distinct")
		}
		seen[strings.ToLower(n)] = true
		if i > 0 && isColour(n) {
			return fieldError("option_names", "Colour must be the first option (BR-040)")
		}
	}
	keys := map[string]bool{}
	for i, r := range rows {
		if len(r.OptionValues) != len(names) {
			return fieldError("rows", fmt.Sprintf("row %d has %d option values for %d option names", i, len(r.OptionValues), len(names)))
		}
		k := gridKey(r.OptionValues)
		if keys[k] {
			return fieldError("rows", fmt.Sprintf("row %d repeats option values %v", i, r.OptionValues))
		}
		keys[k] = true
	}
	return nil
}

func isColour(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "colour", "color", "warna":
		return true
	}
	return false
}

// gridKey identifies a combination. \x1f cannot appear in a typed value.
func gridKey(values []string) string { return strings.Join(values, "\x1f") }

// changes reports whether applying f to v would change anything.
func changes(v Variant, f VariantFields) bool {
	neq := func(a, b *string) bool { return (a == nil) != (b == nil) || (a != nil && *a != *b) }
	switch {
	case f.ClearSKU && v.Sku != nil, f.SKU != nil && neq(f.SKU, v.Sku):
		return true
	case f.ClearBarcode && v.Barcode != nil, f.Barcode != nil && neq(f.Barcode, v.Barcode):
		return true
	case f.RegularPrice != nil && *f.RegularPrice != v.RegularPriceAmount:
		return true
	case f.ClearSalePrice && v.SalePriceAmount != nil,
		f.SalePrice != nil && (v.SalePriceAmount == nil || *f.SalePrice != *v.SalePriceAmount):
		return true
	case f.ClearSaleStarts && v.SaleStartsAt != nil,
		f.SaleStartsAt != nil && (v.SaleStartsAt == nil || !f.SaleStartsAt.Equal(*v.SaleStartsAt)):
		return true
	case f.ClearSale && v.SaleEndsAt != nil,
		f.SaleEndsAt != nil && (v.SaleEndsAt == nil || !f.SaleEndsAt.Equal(*v.SaleEndsAt)):
		return true
	case f.WeightGrams != nil && *f.WeightGrams != v.WeightGrams:
		return true
	}
	return false
}

func orNone(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
