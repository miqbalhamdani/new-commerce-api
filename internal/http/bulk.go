package httpapi

import (
	"net/http"
	"strconv"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/catalog"
	apperrors "github.com/miqbalhamdani/new-commerce-api/internal/platform/errors"
)

// Bulk upsert: 04-api-spec.md §7.5 (P1-072).

type bulkItemBody struct {
	SKU    optional[string] `json:"sku"`
	Title  optional[string] `json:"title"`
	Status optional[string] `json:"status"`
	variantFieldsBody
}

func (s *Server) BulkProducts(w http.ResponseWriter, r *http.Request) {
	requirePermission(auth.PermProductsWrite, requirePermission(auth.PermVariantsWrite, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			OnConflict optional[string]         `json:"on_conflict"`
			Items      optional[[]bulkItemBody] `json:"items"`
		}
		if !decodeJSON(w, r, &body) {
			return
		}
		if !body.OnConflict.Set || (body.OnConflict.Value != "update" && body.OnConflict.Value != "error") {
			writeError(w, r, fieldErr("on_conflict", "on_conflict is update or error"))
			return
		}
		if !body.Items.Set || body.Items.Null {
			writeError(w, r, fieldErr("items", "items is required"))
			return
		}
		if len(body.Items.Value) > catalog.MaxBulk {
			writeError(w, r, fieldErr("items", "at most "+strconv.Itoa(catalog.MaxBulk)+" items per request"))
			return
		}
		items := make([]catalog.BulkItem, 0, len(body.Items.Value))
		for i, it := range body.Items.Value {
			if it.SKU.Null || it.Title.Null || it.Status.Null || it.RegularPrice.Null || it.WeightGrams.Null {
				writeError(w, r, apperrors.ValidationFailed("items["+strconv.Itoa(i)+"]: sku, title, status, regular_price and weight_grams cannot be null").
					WithFields(apperrors.Field{Name: "items", Detail: "null", Extra: map[string]any{"index": i}}))
				return
			}
			item := catalog.BulkItem{Fields: it.fields()}
			if it.SKU.Set {
				item.SKU = &it.SKU.Value
			}
			if it.Title.Set {
				item.Title = &it.Title.Value
			}
			if it.Status.Set {
				item.Status = &it.Status.Value
			}
			items = append(items, item)
		}
		res, err := s.catalog.Bulk(r.Context(), items, body.OnConflict.Value)
		if err != nil {
			writeError(w, r, err)
			return
		}
		out := BulkResult{Created: res.Created, Updated: res.Updated, Failed: res.Failed,
			Results: make([]BulkRowResult, 0, len(res.Results))}
		for _, rr := range res.Results {
			item := BulkRowResult{Index: rr.Index, Sku: rr.SKU, Status: BulkRowResultStatus(rr.Status), VariantId: rr.VariantID}
			if rr.Code != "" {
				code := ErrorCode(rr.Code)
				item.Code, item.Detail = &code, &rr.Detail
			}
			out.Results = append(out.Results, item)
		}
		writeJSON(w, http.StatusOK, out)
	}))(w, r)
}
