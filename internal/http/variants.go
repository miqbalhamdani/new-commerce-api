package httpapi

import (
	"net/http"
	"time"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/catalog"
)

// Variants: 04-api-spec.md §7.2 (P1-029).

// variantReadOnly are refused like server-managed fields: price and on_sale
// come from variant_price() (BR-046), option_values only from the matrix.
var variantReadOnly = []string{"price", "on_sale", "product_id", "currency"}

type variantFieldsBody struct {
	SKU          optional[string]    `json:"sku"`
	Barcode      optional[string]    `json:"barcode"`
	RegularPrice optional[int64]     `json:"regular_price"`
	SalePrice    optional[int64]     `json:"sale_price"`
	SaleStartsAt optional[time.Time] `json:"sale_starts_at"`
	SaleEndsAt   optional[time.Time] `json:"sale_ends_at"`
	WeightGrams  optional[int32]     `json:"weight_grams"`
}

type variantCreateBody struct {
	OptionValues optional[[]string] `json:"option_values"`
	variantFieldsBody
}

// fields turns a body into the service's view of it. On create every null is
// already refused; on PATCH a null clears a nullable column.
func (b variantFieldsBody) fields() catalog.VariantFields {
	f := catalog.VariantFields{ClearSKU: b.SKU.Null, ClearBarcode: b.Barcode.Null, ClearSalePrice: b.SalePrice.Null,
		ClearSaleStarts: b.SaleStartsAt.Null, ClearSale: b.SaleEndsAt.Null}
	if b.SKU.Set && !b.SKU.Null {
		f.SKU = &b.SKU.Value
	}
	if b.Barcode.Set && !b.Barcode.Null {
		f.Barcode = &b.Barcode.Value
	}
	if b.RegularPrice.Set && !b.RegularPrice.Null {
		f.RegularPrice = &b.RegularPrice.Value
	}
	if b.SalePrice.Set && !b.SalePrice.Null {
		f.SalePrice = &b.SalePrice.Value
	}
	if b.SaleStartsAt.Set && !b.SaleStartsAt.Null {
		f.SaleStartsAt = &b.SaleStartsAt.Value
	}
	if b.SaleEndsAt.Set && !b.SaleEndsAt.Null {
		f.SaleEndsAt = &b.SaleEndsAt.Value
	}
	if b.WeightGrams.Set && !b.WeightGrams.Null {
		f.WeightGrams = &b.WeightGrams.Value
	}
	return f
}

func (b variantFieldsBody) nulls() map[string]bool {
	return map[string]bool{"sku": b.SKU.Null, "barcode": b.Barcode.Null, "regular_price": b.RegularPrice.Null,
		"sale_price": b.SalePrice.Null, "sale_starts_at": b.SaleStartsAt.Null, "sale_ends_at": b.SaleEndsAt.Null,
		"weight_grams": b.WeightGrams.Null}
}

func (s *Server) ListVariants(w http.ResponseWriter, r *http.Request, id Id, params ListVariantsParams) {
	requirePermission(auth.PermVariantsRead, func(w http.ResponseWriter, r *http.Request) {
		rows, err := s.catalog.ListVariants(r.Context(), id, params.Archived != nil && *params.Archived)
		if err != nil {
			writeError(w, r, err)
			return
		}
		out := VariantList{Data: make([]Variant, 0, len(rows))}
		for _, v := range rows {
			out.Data = append(out.Data, variantOut(v))
		}
		writeJSON(w, http.StatusOK, out)
	})(w, r)
}

func (s *Server) CreateVariant(w http.ResponseWriter, r *http.Request, id Id) {
	requirePermission(auth.PermVariantsWrite, func(w http.ResponseWriter, r *http.Request) {
		var body variantCreateBody
		if !decodeJSON(w, r, &body, variantReadOnly...) {
			return
		}
		nulls := body.nulls()
		nulls["option_values"] = body.OptionValues.Null
		if err := rejectNull(nulls); err != nil {
			writeError(w, r, err)
			return
		}
		v, err := s.catalog.CreateVariant(r.Context(), id, body.OptionValues.Value, body.fields())
		if err != nil {
			writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusCreated, variantOut(v))
	})(w, r)
}

func (s *Server) UpdateVariant(w http.ResponseWriter, r *http.Request, id Id, params UpdateVariantParams) {
	requirePermission(auth.PermVariantsWrite, func(w http.ResponseWriter, r *http.Request) {
		var body variantFieldsBody
		if !decodeJSON(w, r, &body, append(variantReadOnly, "option_values")...) {
			return
		}
		if err := rejectNull(map[string]bool{"regular_price": body.RegularPrice.Null, "weight_grams": body.WeightGrams.Null}); err != nil {
			writeError(w, r, err)
			return
		}
		v, err := s.catalog.UpdateVariant(r.Context(), id, params.IfMatch, body.fields())
		if err != nil {
			writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, variantOut(v))
	})(w, r)
}

func (s *Server) ArchiveVariant(w http.ResponseWriter, r *http.Request, id Id) {
	requirePermission(auth.PermVariantsWrite, func(w http.ResponseWriter, r *http.Request) {
		if err := s.catalog.ArchiveVariant(r.Context(), id); err != nil {
			writeError(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})(w, r)
}

func variantOut(v catalog.Variant) Variant {
	return Variant{Id: v.ID, ProductId: v.ProductID, Version: int(v.Version), OptionValues: v.OptionValues,
		Sku: v.Sku, Barcode: v.Barcode, RegularPrice: v.RegularPriceAmount, SalePrice: v.SalePriceAmount,
		SaleStartsAt: v.SaleStartsAt, SaleEndsAt: v.SaleEndsAt, Price: v.Price, OnSale: catalog.OnSale(v),
		WeightGrams: int(v.WeightGrams), ArchivedAt: v.ArchivedAt, CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt}
}
