package httpapi

import (
	"net/http"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/blog"
)

// Blog categories: 04-api-spec.md §13.1 (P1-602).

type blogCategoryBody struct {
	Name     optional[string] `json:"name"`
	Slug     optional[string] `json:"slug"`
	Position optional[int32]  `json:"position"`
}

func (s *Server) ListBlogCategories(w http.ResponseWriter, r *http.Request, params ListBlogCategoriesParams) {
	requirePermission(auth.PermBlogRead, func(w http.ResponseWriter, r *http.Request) {
		rows, err := s.blog.ListCategories(r.Context(), params.Archived != nil && *params.Archived)
		if err != nil {
			writeError(w, r, err)
			return
		}
		list := BlogCategoryList{Data: make([]BlogCategory, 0, len(rows))}
		for _, c := range rows {
			list.Data = append(list.Data, blogCategoryOut(c))
		}
		writeJSON(w, http.StatusOK, list)
	})(w, r)
}

func (s *Server) CreateBlogCategory(w http.ResponseWriter, r *http.Request) {
	requirePermission(auth.PermBlogWrite, func(w http.ResponseWriter, r *http.Request) {
		var body blogCategoryBody
		if !decodeJSON(w, r, &body) {
			return
		}
		if err := rejectNull(map[string]bool{"name": body.Name.Null, "slug": body.Slug.Null,
			"position": body.Position.Null}); err != nil {
			writeError(w, r, err)
			return
		}
		var slug *string
		if body.Slug.Set {
			slug = &body.Slug.Value
		}
		c, err := s.blog.CreateCategory(r.Context(), body.Name.Value, slug, body.Position.Value)
		if err != nil {
			writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusCreated, blogCategoryOut(c))
	})(w, r)
}

func (s *Server) UpdateBlogCategory(w http.ResponseWriter, r *http.Request, id Id) {
	requirePermission(auth.PermBlogWrite, func(w http.ResponseWriter, r *http.Request) {
		var body blogCategoryBody
		if !decodeJSON(w, r, &body) {
			return
		}
		if err := rejectNull(map[string]bool{"name": body.Name.Null, "slug": body.Slug.Null,
			"position": body.Position.Null}); err != nil {
			writeError(w, r, err)
			return
		}
		var p blog.CategoryPatch
		if body.Name.Set {
			p.Name = &body.Name.Value
		}
		if body.Slug.Set {
			p.Slug = &body.Slug.Value
		}
		if body.Position.Set {
			p.Position = &body.Position.Value
		}
		c, err := s.blog.UpdateCategory(r.Context(), id, p)
		if err != nil {
			writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, blogCategoryOut(c))
	})(w, r)
}

func (s *Server) ArchiveBlogCategory(w http.ResponseWriter, r *http.Request, id Id) {
	requirePermission(auth.PermBlogWrite, func(w http.ResponseWriter, r *http.Request) {
		if err := s.blog.ArchiveCategory(r.Context(), id); err != nil {
			writeError(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})(w, r)
}

func blogCategoryOut(c blog.Category) BlogCategory {
	return BlogCategory{Id: c.ID, Name: c.Name, Slug: c.Slug, Position: int(c.Position),
		ArchivedAt: c.ArchivedAt, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt}
}
