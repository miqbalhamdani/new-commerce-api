package httpapi

import (
	"net/http"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/catalog"
)

// Variant matrix: 04-api-spec.md §7.3 (P1-040, P1-041).

type matrixBody struct {
	OptionNames    optional[[]string]            `json:"option_names"`
	Rows           optional[[]variantCreateBody] `json:"rows"`
	ArchiveMissing optional[bool]                `json:"archive_missing"`
}

func (s *Server) PutVariantMatrix(w http.ResponseWriter, r *http.Request, id Id, params PutVariantMatrixParams) {
	requirePermission(auth.PermVariantsWrite, func(w http.ResponseWriter, r *http.Request) {
		var body matrixBody
		if !decodeJSON(w, r, &body) {
			return
		}
		for name, o := range map[string]struct{ Set, Null bool }{
			"option_names":    {body.OptionNames.Set, body.OptionNames.Null},
			"rows":            {body.Rows.Set, body.Rows.Null},
			"archive_missing": {body.ArchiveMissing.Set, body.ArchiveMissing.Null},
		} {
			if !o.Set || o.Null {
				writeError(w, r, fieldErr(name, name+" is required"))
				return
			}
		}
		rows := make([]catalog.MatrixRow, 0, len(body.Rows.Value))
		for _, row := range body.Rows.Value {
			if !row.OptionValues.Set || row.OptionValues.Null || row.RegularPrice.Null || row.WeightGrams.Null {
				writeError(w, r, fieldErr("rows", "every row needs option_values; regular_price and weight_grams cannot be null"))
				return
			}
			rows = append(rows, catalog.MatrixRow{OptionValues: row.OptionValues.Value, Fields: row.fields()})
		}
		res, err := s.catalog.SaveMatrix(r.Context(), id, params.IfMatch, body.OptionNames.Value, rows, body.ArchiveMissing.Value)
		if err != nil {
			writeError(w, r, err)
			return
		}
		out := VariantMatrixResult{ProductVersion: res.ProductVersion, Created: res.Created, Updated: res.Updated,
			Restored: res.Restored, Unchanged: res.Unchanged, Archived: res.Archived, Failed: res.Failed,
			ArchivedVariantIds: res.ArchivedIDs, Results: make([]VariantMatrixRowResult, 0, len(res.Results))}
		if out.ArchivedVariantIds == nil {
			out.ArchivedVariantIds = res.ArchivedIDs[:0:0]
		}
		for _, rr := range res.Results {
			item := VariantMatrixRowResult{OptionValues: rr.OptionValues, Status: VariantMatrixRowResultStatus(rr.Status),
				VariantId: rr.VariantID}
			if rr.Code != "" {
				code := ErrorCode(rr.Code)
				item.Code, item.Detail = &code, &rr.Detail
			}
			out.Results = append(out.Results, item)
		}
		writeJSON(w, http.StatusOK, out)
	})(w, r)
}
