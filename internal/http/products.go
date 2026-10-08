package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/catalog"
)

// Products: 04-api-spec.md §7.1 (P1-028).

type productCreateBody struct {
	Title       optional[string]         `json:"title"`
	Description optional[string]         `json:"description"`
	BrandID     optional[uuid.UUID]      `json:"brand_id"`
	CategoryIDs optional[[]uuid.UUID]    `json:"category_ids"`
	Attributes  optional[map[string]any] `json:"attributes"`
}

type productUpdateBody struct {
	Title       optional[string]         `json:"title"`
	Slug        optional[string]         `json:"slug"`
	Description optional[string]         `json:"description"`
	BrandID     optional[uuid.UUID]      `json:"brand_id"`
	CategoryIDs optional[[]uuid.UUID]    `json:"category_ids"`
	Attributes  optional[map[string]any] `json:"attributes"`
	Status      optional[string]         `json:"status"`
}

func (s *Server) CreateProduct(w http.ResponseWriter, r *http.Request) {
	requirePermission(auth.PermProductsWrite, func(w http.ResponseWriter, r *http.Request) {
		var body productCreateBody
		if !decodeJSON(w, r, &body, "option_names", "status", "slug") {
			return
		}
		if err := rejectNull(map[string]bool{"title": body.Title.Null, "description": body.Description.Null,
			"brand_id": body.BrandID.Null, "category_ids": body.CategoryIDs.Null, "attributes": body.Attributes.Null}); err != nil {
			writeError(w, r, err)
			return
		}
		in := catalog.ProductInput{Title: body.Title.Value, CategoryIDs: body.CategoryIDs.Value, Attributes: body.Attributes.Value}
		if body.Description.Set {
			in.Description = &body.Description.Value
		}
		if body.BrandID.Set {
			in.BrandID = &body.BrandID.Value
		}
		p, err := s.catalog.CreateProduct(r.Context(), in)
		if err != nil {
			writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusCreated, productOut(p))
	})(w, r)
}

func (s *Server) GetProduct(w http.ResponseWriter, r *http.Request, id Id) {
	requirePermission(auth.PermProductsRead, func(w http.ResponseWriter, r *http.Request) {
		p, err := s.catalog.GetProduct(r.Context(), id)
		if err != nil {
			writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, productOut(p))
	})(w, r)
}

// UpdateProduct is a PATCH at the version in If-Match (BR-010). option_names
// changes only through the variant matrix, so it is refused here like a
// server-managed field.
func (s *Server) UpdateProduct(w http.ResponseWriter, r *http.Request, id Id, params UpdateProductParams) {
	requirePermission(auth.PermProductsWrite, func(w http.ResponseWriter, r *http.Request) {
		var body productUpdateBody
		if !decodeJSON(w, r, &body, "option_names") {
			return
		}
		if err := rejectNull(map[string]bool{"title": body.Title.Null, "slug": body.Slug.Null,
			"category_ids": body.CategoryIDs.Null, "attributes": body.Attributes.Null, "status": body.Status.Null}); err != nil {
			writeError(w, r, err)
			return
		}
		p := catalog.ProductPatch{ClearDescription: body.Description.Null, ClearBrand: body.BrandID.Null}
		if body.Title.Set {
			p.Title = &body.Title.Value
		}
		if body.Slug.Set {
			p.Slug = &body.Slug.Value
		}
		if body.Description.Set && !body.Description.Null {
			p.Description = &body.Description.Value
		}
		if body.BrandID.Set && !body.BrandID.Null {
			p.BrandID = &body.BrandID.Value
		}
		if body.CategoryIDs.Set {
			p.CategoryIDs = &body.CategoryIDs.Value
		}
		if body.Attributes.Set {
			p.Attributes = body.Attributes.Value
		}
		if body.Status.Set {
			if body.Status.Value != "draft" && body.Status.Value != "active" {
				writeError(w, r, fieldErr("status", "status is draft or active; archive with DELETE"))
				return
			}
			p.Status = &body.Status.Value
		}
		out, err := s.catalog.UpdateProduct(r.Context(), id, params.IfMatch, p)
		if err != nil {
			writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, productOut(out))
	})(w, r)
}

func (s *Server) ArchiveProduct(w http.ResponseWriter, r *http.Request, id Id) {
	requirePermission(auth.PermProductsWrite, func(w http.ResponseWriter, r *http.Request) {
		if err := s.catalog.ArchiveProduct(r.Context(), id); err != nil {
			writeError(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})(w, r)
}

func productOut(v catalog.ProductView) Product {
	p := Product{Id: v.ID, Version: int(v.Version), Title: v.Title, Slug: v.Slug, Description: v.Description,
		Status: ProductStatus(v.Status), OptionNames: v.OptionNames, VariantCount: v.VariantCount,
		ArchivedAt: v.ArchivedAt, CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt,
		Categories: make([]CategoryRef, 0, len(v.Categories)), Media: mediaOut(v.Media)}
	if p.OptionNames == nil {
		p.OptionNames = []string{}
	}
	if v.Brand != nil {
		p.Brand = &Ref{Id: v.Brand.ID, Name: v.Brand.Name}
	}
	for _, c := range v.Categories {
		p.Categories = append(p.Categories, CategoryRef{Id: c.ID, Kind: CategoryKind(c.Kind), Name: c.Name, Path: c.Path})
	}
	_ = json.Unmarshal(v.Attributes, &p.Attributes)
	if p.Attributes == nil {
		p.Attributes = map[string]any{}
	}
	return p
}

// mediaOut renders a product's images. ponytail: empty until P1-043.
func mediaOut([]catalog.Media) []Media { return []Media{} }
