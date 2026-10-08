package httpapi

import (
	"net/http"

	"github.com/google/uuid"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/catalog"
)

// Categories: 04-api-spec.md §6.2 (P1-024).

type categoryCreateBody struct {
	Name     optional[string]    `json:"name"`
	ParentID optional[uuid.UUID] `json:"parent_id"`
	Kind     optional[string]    `json:"kind"`
}

type categoryUpdateBody struct {
	Name     optional[string]    `json:"name"`
	ParentID optional[uuid.UUID] `json:"parent_id"`
}

func (s *Server) ListCategories(w http.ResponseWriter, r *http.Request, params ListCategoriesParams) {
	requirePermission(auth.PermCategoriesRead, func(w http.ResponseWriter, r *http.Request) {
		var kind *string
		if params.Kind != nil && !params.Kind.Valid() {
			writeError(w, r, fieldErr("kind", "kind is one of category, series, collection, activity, custom"))
			return
		}
		if params.Kind != nil {
			k := string(*params.Kind)
			kind = &k
		}
		rows, err := s.catalog.ListCategories(r.Context(), kind, params.ParentId, params.Depth)
		if err != nil {
			writeError(w, r, err)
			return
		}
		out := CategoryList{Data: make([]Category, 0, len(rows))}
		for _, c := range rows {
			out.Data = append(out.Data, categoryOut(c))
		}
		writeJSON(w, http.StatusOK, out)
	})(w, r)
}

func (s *Server) CreateCategory(w http.ResponseWriter, r *http.Request) {
	requirePermission(auth.PermCategoriesWrite, func(w http.ResponseWriter, r *http.Request) {
		var body categoryCreateBody
		if !decodeJSON(w, r, &body) {
			return
		}
		if err := rejectNull(map[string]bool{"name": body.Name.Null, "parent_id": body.ParentID.Null, "kind": body.Kind.Null}); err != nil {
			writeError(w, r, err)
			return
		}
		kind := "category"
		if body.Kind.Set {
			if !CategoryKind(body.Kind.Value).Valid() {
				writeError(w, r, fieldErr("kind", "kind is one of category, series, collection, activity, custom"))
				return
			}
			kind = body.Kind.Value
		}
		var parent *uuid.UUID
		if body.ParentID.Set {
			parent = &body.ParentID.Value
		}
		c, err := s.catalog.CreateCategory(r.Context(), body.Name.Value, kind, parent)
		if err != nil {
			writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusCreated, categoryOut(c))
	})(w, r)
}

func (s *Server) GetCategory(w http.ResponseWriter, r *http.Request, id Id) {
	requirePermission(auth.PermCategoriesRead, func(w http.ResponseWriter, r *http.Request) {
		d, err := s.catalog.GetCategory(r.Context(), id)
		if err != nil {
			writeError(w, r, err)
			return
		}
		c := categoryOut(d.Category)
		writeJSON(w, http.StatusOK, CategoryDetail{
			Id: c.Id, Kind: c.Kind, Name: c.Name, ParentId: c.ParentId, Path: c.Path,
			ArchivedAt: c.ArchivedAt, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
			DescendantCount: d.DescendantCount, ProductCount: d.ProductCount,
		})
	})(w, r)
}

// UpdateCategory renames or moves. kind is not in the body type, so sending
// it is unknown_field; it never changes.
func (s *Server) UpdateCategory(w http.ResponseWriter, r *http.Request, id Id) {
	requirePermission(auth.PermCategoriesWrite, func(w http.ResponseWriter, r *http.Request) {
		var body categoryUpdateBody
		if !decodeJSON(w, r, &body) {
			return
		}
		if err := rejectNull(map[string]bool{"name": body.Name.Null}); err != nil {
			writeError(w, r, err)
			return
		}
		u := catalog.CategoryUpdate{ClearParent: body.ParentID.Null}
		if body.Name.Set {
			u.Name = &body.Name.Value
		}
		if body.ParentID.Set && !body.ParentID.Null {
			u.Parent = &body.ParentID.Value
		}
		c, err := s.catalog.UpdateCategory(r.Context(), id, u)
		if err != nil {
			writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, categoryOut(c))
	})(w, r)
}

func (s *Server) ArchiveCategory(w http.ResponseWriter, r *http.Request, id Id) {
	requirePermission(auth.PermCategoriesWrite, func(w http.ResponseWriter, r *http.Request) {
		if err := s.catalog.ArchiveCategory(r.Context(), id); err != nil {
			writeError(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})(w, r)
}

func categoryOut(c catalog.Category) Category {
	return Category{Id: c.ID, Kind: CategoryKind(c.Kind), Name: c.Name, ParentId: c.ParentID, Path: c.Path,
		ArchivedAt: c.ArchivedAt, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt}
}
