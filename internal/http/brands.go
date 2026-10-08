package httpapi

import (
	"net/http"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/catalog"
)

// Brands: 04-api-spec.md §6.1 (P1-021).

type brandBody struct {
	Name optional[string] `json:"name"`
}

func (s *Server) ListBrands(w http.ResponseWriter, r *http.Request, params ListBrandsParams) {
	requirePermission(auth.PermBrandsRead, func(w http.ResponseWriter, r *http.Request) {
		var after *catalog.BrandCursor
		if params.Cursor != nil {
			after = &catalog.BrandCursor{}
			if err := decodeCursor(params.Cursor, after); err != nil {
				writeError(w, r, err)
				return
			}
		}
		archived := params.Archived != nil && *params.Archived
		rows, next, err := s.catalog.ListBrands(r.Context(), params.Q, archived, after, pageLimit(params.Limit))
		if err != nil {
			writeError(w, r, err)
			return
		}
		page := BrandPage{Data: make([]Brand, 0, len(rows))}
		for _, b := range rows {
			page.Data = append(page.Data, brandOut(b))
		}
		if next != nil {
			page.NextCursor = encodeCursor(next)
		}
		writeJSON(w, http.StatusOK, page)
	})(w, r)
}

func (s *Server) CreateBrand(w http.ResponseWriter, r *http.Request) {
	requirePermission(auth.PermBrandsWrite, func(w http.ResponseWriter, r *http.Request) {
		var body brandBody
		if !decodeJSON(w, r, &body, "slug") {
			return
		}
		if err := rejectNull(map[string]bool{"name": body.Name.Null}); err != nil {
			writeError(w, r, err)
			return
		}
		b, err := s.catalog.CreateBrand(r.Context(), body.Name.Value)
		if err != nil {
			writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusCreated, brandOut(b))
	})(w, r)
}

func (s *Server) GetBrand(w http.ResponseWriter, r *http.Request, id Id) {
	requirePermission(auth.PermBrandsRead, func(w http.ResponseWriter, r *http.Request) {
		b, err := s.catalog.GetBrand(r.Context(), id)
		if err != nil {
			writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, brandOut(b))
	})(w, r)
}

// UpdateBrand renames. name is the only writable field, so an empty body
// changes nothing and returns the brand as it is.
func (s *Server) UpdateBrand(w http.ResponseWriter, r *http.Request, id Id) {
	requirePermission(auth.PermBrandsWrite, func(w http.ResponseWriter, r *http.Request) {
		var body brandBody
		if !decodeJSON(w, r, &body, "slug") {
			return
		}
		if err := rejectNull(map[string]bool{"name": body.Name.Null}); err != nil {
			writeError(w, r, err)
			return
		}
		var b catalog.Brand
		var err error
		if body.Name.Set {
			b, err = s.catalog.RenameBrand(r.Context(), id, body.Name.Value)
		} else {
			b, err = s.catalog.GetBrand(r.Context(), id)
		}
		if err != nil {
			writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, brandOut(b))
	})(w, r)
}

func (s *Server) ArchiveBrand(w http.ResponseWriter, r *http.Request, id Id) {
	requirePermission(auth.PermBrandsWrite, func(w http.ResponseWriter, r *http.Request) {
		if err := s.catalog.ArchiveBrand(r.Context(), id); err != nil {
			writeError(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})(w, r)
}

func brandOut(b catalog.Brand) Brand {
	return Brand{Id: b.ID, Name: b.Name, Slug: b.Slug, ArchivedAt: b.ArchivedAt,
		CreatedAt: b.CreatedAt, UpdatedAt: b.UpdatedAt}
}
