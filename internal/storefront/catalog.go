package storefront

import (
	"encoding/json"
	"net/http"

	"github.com/miqbalhamdani/new-commerce-api/internal/catalog"
	"github.com/miqbalhamdani/new-commerce-api/internal/httpkit"
	apperrors "github.com/miqbalhamdani/new-commerce-api/internal/platform/errors"
)

// Catalog: 04-api-spec.md §11.4 (P1-203). Key only; everything here is
// public by design.

var productSorts = map[ListProductsParamsSort]bool{"-updated_at": true, "price_min": true, "-price_min": true, "title": true}

var categoryKinds = map[CategoryKind]bool{"category": true, "series": true, "collection": true, "activity": true, "custom": true}

func (s *Server) ListProducts(w http.ResponseWriter, r *http.Request, params ListProductsParams) {
	f := catalog.ShopFilter{CategoryPath: params.Category, BrandSlug: params.Brand, Q: params.Q,
		Limit: httpkit.PageLimit(params.Limit)}
	if params.Sort != nil {
		if !productSorts[*params.Sort] {
			httpkit.WriteError(w, r, fieldError("sort", "sort is one of -updated_at, price_min, -price_min, title"))
			return
		}
		f.Sort = string(*params.Sort)
	}
	if params.Cursor != nil {
		f.After = &catalog.ShopCursor{}
		if err := httpkit.DecodeCursor(params.Cursor, f.After); err != nil {
			httpkit.WriteError(w, r, err)
			return
		}
	}
	rows, next, err := s.catalog.ShopProducts(r.Context(), f)
	if err != nil {
		httpkit.WriteError(w, r, err)
		return
	}
	page := ProductPage{Data: make([]ProductListItem, 0, len(rows))}
	for _, p := range rows {
		item := ProductListItem{Slug: p.Slug, Title: p.Title, PriceMin: p.PriceMin, PriceMax: p.PriceMax}
		if p.Brand != nil {
			item.Brand = &BrandRef{Slug: p.Brand.Slug, Name: p.Brand.Name}
		}
		if p.Image != nil {
			item.Image = &Image{Url: p.Image.URL, Sizes: p.Image.Sizes}
		}
		page.Data = append(page.Data, item)
	}
	if next != nil {
		page.NextCursor = httpkit.EncodeCursor(next)
	}
	httpkit.WriteJSON(w, http.StatusOK, page)
}

func (s *Server) GetProduct(w http.ResponseWriter, r *http.Request, slug string) {
	d, err := s.catalog.ShopProduct(r.Context(), slug)
	if err != nil {
		httpkit.WriteError(w, r, err)
		return
	}
	out := ProductDetail{Id: d.ID, Slug: d.Slug, Title: d.Title, Description: d.Description,
		OptionNames: d.OptionNames, UpdatedAt: d.UpdatedAt, Attributes: map[string]any{},
		Categories: make([]ProductCategory, 0, len(d.Categories)),
		Images:     make([]ProductImage, 0, len(d.Images)),
		Variants:   make([]ProductVariant, 0, len(d.Variants))}
	if out.OptionNames == nil {
		out.OptionNames = []string{}
	}
	if len(d.Attributes) > 0 {
		if err := json.Unmarshal(d.Attributes, &out.Attributes); err != nil {
			httpkit.WriteError(w, r, err)
			return
		}
	}
	if d.BrandSlug != nil {
		out.Brand = &BrandRef{Slug: *d.BrandSlug, Name: *d.BrandName}
	}
	for _, c := range d.Categories {
		out.Categories = append(out.Categories, ProductCategory{Kind: CategoryKind(c.Kind), Name: c.Name, Path: c.Path})
	}
	for _, m := range d.Images {
		out.Images = append(out.Images, ProductImage{VariantId: m.VariantID, Url: m.URL, Sizes: m.Sizes})
	}
	for _, v := range d.Variants {
		out.Variants = append(out.Variants, ProductVariant{Id: v.ID, Sku: v.Sku, OptionValues: v.OptionValues,
			Price: v.PriceAmount, RegularPrice: v.RegularPriceAmount, OnSale: v.OnSale, WeightGrams: int(v.WeightGrams)})
	}
	httpkit.WriteJSON(w, http.StatusOK, out)
}

func (s *Server) ListCategories(w http.ResponseWriter, r *http.Request, params ListCategoriesParams) {
	var kind *string
	if params.Kind != nil {
		if !categoryKinds[*params.Kind] {
			httpkit.WriteError(w, r, fieldError("kind", "kind is one of category, series, collection, activity, custom"))
			return
		}
		k := string(*params.Kind)
		kind = &k
	}
	rows, err := s.catalog.ShopCategories(r.Context(), kind)
	if err != nil {
		httpkit.WriteError(w, r, err)
		return
	}
	list := CategoryList{Data: make([]Category, 0, len(rows))}
	for _, c := range rows {
		list.Data = append(list.Data, Category{Id: c.ID, ParentId: c.ParentID, Kind: CategoryKind(c.Kind), Name: c.Name, Path: c.Path})
	}
	httpkit.WriteJSON(w, http.StatusOK, list)
}

func (s *Server) ListBrands(w http.ResponseWriter, r *http.Request) {
	rows, err := s.catalog.ShopBrands(r.Context())
	if err != nil {
		httpkit.WriteError(w, r, err)
		return
	}
	list := BrandList{Data: make([]BrandRef, 0, len(rows))}
	for _, b := range rows {
		list.Data = append(list.Data, BrandRef{Slug: b.Slug, Name: b.Name})
	}
	httpkit.WriteJSON(w, http.StatusOK, list)
}

func fieldError(field, detail string) error {
	return apperrors.ValidationFailed(detail).WithFields(apperrors.Field{Name: field, Detail: detail})
}
