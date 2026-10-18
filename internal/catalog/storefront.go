package catalog

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/db/sqlcgen"
	apperrors "github.com/miqbalhamdani/new-commerce-api/internal/platform/errors"
)

// The storefront's catalog reads (P1-203, 04-api-spec.md §11.4). Products and
// variants come only from the storefront views, which encode "visible"
// (BR-080, BR-081); nothing here reads the products or variants tables.

// ShopFilter is GET /v1/storefront/products.
type ShopFilter struct {
	CategoryPath *string
	BrandSlug    *string
	Q            *string
	Sort         string // "-updated_at" (default), "price_min", "-price_min", "title"
	After        *ShopCursor
	Page         int // 1-based; 0 means keyset paging with After (P1-230)
	Limit        int
}

// ShopPage is one page of the product list: the rows, the keyset position
// after them (nil on the last page), and how many products match in all.
type ShopPage struct {
	Products []ShopProduct
	Next     *ShopCursor
	Total    int64
}

// ShopCursor is the keyset position after a page: the sort key and the id
// that breaks ties.
type ShopCursor struct {
	At    *time.Time `json:"t,omitempty"`
	Price *int64     `json:"p,omitempty"`
	Title *string    `json:"s,omitempty"`
	ID    uuid.UUID  `json:"i"`
}

type ShopBrand struct{ Slug, Name string }

// ShopImage is an image's URL and its renditions by width, once the worker
// has made them (BR-052).
type ShopImage struct {
	VariantID *uuid.UUID
	URL       string
	Sizes     map[string]string
}

type ShopProduct struct {
	ID                   uuid.UUID
	Slug, Title          string
	Brand                *ShopBrand
	Categories           []sqlcgen.StorefrontProductCategoriesRow
	PriceMin, PriceMax   int64
	WeightMin, WeightMax int32
	Image                *ShopImage
	UpdatedAt            time.Time
}

// categoryPath is an ltree path as categories store it; anything else would
// be a SQL error rather than a 422.
var categoryPath = regexp.MustCompile(`^[a-z0-9_]+(\.[a-z0-9_]+)*$`)

// ShopProducts is the storefront product list. Only products with a visible
// variant appear: one without has no price to show or sort by (03-erd.md §4).
// price_min and price_max are variant_price(), sales included (BR-046).
// f.Page pages by offset for a numbered pager instead of by keyset; the total
// is counted either way (P1-230).
func (s *Service) ShopProducts(ctx context.Context, f ShopFilter) (ShopPage, error) {
	var (
		where []string
		args  []any
	)
	arg := func(v any) string { args = append(args, v); return fmt.Sprintf("$%d", len(args)) }

	if f.CategoryPath != nil {
		if !categoryPath.MatchString(*f.CategoryPath) {
			return ShopPage{}, fieldError("category", "category is a category path such as apparel.tees")
		}
		where = append(where, `sp.id IN (SELECT pc.product_id FROM product_categories pc
			JOIN categories c ON c.id = pc.category_id
			WHERE c.archived_at IS NULL AND c.path <@ `+arg(*f.CategoryPath)+`::ltree)`)
	}
	if f.BrandSlug != nil {
		where = append(where, "sp.brand_id IN (SELECT id FROM brands WHERE archived_at IS NULL AND slug = "+arg(*f.BrandSlug)+")")
	}
	if q := likePattern(f.Q); q != nil {
		where = append(where, "sp.title ILIKE '%' || "+arg(*q)+" || '%'")
	}
	cond := "true"
	if len(where) > 0 {
		cond = strings.Join(where, " AND ")
	}
	// The count takes the filters' arguments only, so it cannot drift from the
	// list's WHERE.
	countSQL := `SELECT count(*) FROM storefront_products sp
	 WHERE ` + cond + ` AND EXISTS (SELECT 1 FROM storefront_variants sv WHERE sv.product_id = sp.id)`
	countArgs := append([]any(nil), args...)
	if f.Page > 0 {
		f.After = nil
	}

	var order, after string
	switch f.Sort {
	case "price_min":
		order = "lo, id"
		if f.After != nil && f.After.Price != nil {
			after = "(lo, id) > (" + arg(*f.After.Price) + ", " + arg(f.After.ID) + ")"
		}
	case "-price_min":
		order = "lo DESC, id DESC"
		if f.After != nil && f.After.Price != nil {
			after = "(lo, id) < (" + arg(*f.After.Price) + ", " + arg(f.After.ID) + ")"
		}
	case "title":
		order = "title, id"
		if f.After != nil && f.After.Title != nil {
			after = "(title, id) > (" + arg(*f.After.Title) + ", " + arg(f.After.ID) + ")"
		}
	default:
		order = "updated_at DESC, id DESC"
		if f.After != nil && f.After.At != nil {
			after = "(updated_at, id) < (" + arg(*f.After.At) + ", " + arg(f.After.ID) + ")"
		}
	}
	if after == "" {
		after = "true"
	}
	limit := "LIMIT " + arg(f.Limit+1)
	if f.Page > 0 {
		limit += " OFFSET " + arg(int64(f.Page-1)*int64(f.Limit))
	}

	// Prices are aggregated over the filtered products, because two of the
	// sorts are by price; the image and brand are then looked up for the
	// page's rows only.
	sql := `WITH priced AS (
	  SELECT sp.id, sp.slug, sp.title, sp.brand_id, sp.updated_at,
	         min(sv.price_amount) AS lo, max(sv.price_amount) AS hi,
	         min(sv.weight_grams) AS wlo, max(sv.weight_grams) AS whi
	    FROM storefront_products sp
	    JOIN storefront_variants sv ON sv.product_id = sp.id
	   WHERE ` + cond + `
	   GROUP BY sp.id, sp.slug, sp.title, sp.brand_id, sp.updated_at
	), page AS (
	  SELECT * FROM priced WHERE ` + after + ` ORDER BY ` + order + ` ` + limit + `
	)
	SELECT p.id, p.slug, p.title, p.updated_at, p.lo, p.hi, p.wlo, p.whi, b.slug, b.name, m.r2_key, m.derivatives
	  FROM page p
	  LEFT JOIN brands b ON b.id = p.brand_id AND b.archived_at IS NULL
	  LEFT JOIN LATERAL (SELECT r2_key, derivatives FROM product_media
	                      WHERE product_id = p.id ORDER BY position, id LIMIT 1) m ON true
	 ORDER BY ` + order

	var (
		out   []ShopProduct
		total int64
	)
	err := s.tx(ctx, func(q *sqlcgen.Queries, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, countSQL, countArgs...).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				p                    ShopProduct
				brandSlug, brandName *string
				imageKey             *string
				derivatives          []byte
			)
			if err := rows.Scan(&p.ID, &p.Slug, &p.Title, &p.UpdatedAt, &p.PriceMin, &p.PriceMax,
				&p.WeightMin, &p.WeightMax, &brandSlug, &brandName, &imageKey, &derivatives); err != nil {
				return err
			}
			if brandSlug != nil {
				p.Brand = &ShopBrand{Slug: *brandSlug, Name: *brandName}
			}
			if imageKey != nil {
				img := s.shopImage(nil, *imageKey, derivatives, "800")
				p.Image = &img
			}
			out = append(out, p)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		return s.shopCategories(ctx, q, out)
	})
	if err != nil {
		return ShopPage{}, err
	}
	if len(out) <= f.Limit {
		return ShopPage{Products: out, Total: total}, nil
	}
	out = out[:f.Limit]
	last := out[f.Limit-1]
	next := &ShopCursor{ID: last.ID}
	switch f.Sort {
	case "price_min", "-price_min":
		next.Price = &last.PriceMin
	case "title":
		next.Title = &last.Title
	default:
		next.At = &last.UpdatedAt
	}
	return ShopPage{Products: out, Next: next, Total: total}, nil
}

// shopCategories fills each listed product's categories with one query for
// the page, ordered as a product's own (StorefrontProductCategories).
func (s *Service) shopCategories(ctx context.Context, q *sqlcgen.Queries, out []ShopProduct) error {
	if len(out) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, len(out))
	at := make(map[uuid.UUID]int, len(out))
	for i, p := range out {
		ids[i], at[p.ID] = p.ID, i
	}
	rows, err := q.StorefrontCategoriesOfProducts(ctx, ids)
	if err != nil {
		return err
	}
	for _, c := range rows {
		i := at[c.ProductID]
		out[i].Categories = append(out[i].Categories,
			sqlcgen.StorefrontProductCategoriesRow{Kind: c.Kind, Name: c.Name, Path: c.Path})
	}
	return nil
}

type ShopProductDetail struct {
	sqlcgen.StorefrontProductBySlugRow
	Categories []sqlcgen.StorefrontProductCategoriesRow
	Images     []ShopImage
	Variants   []sqlcgen.StorefrontVariantsOfProductRow
}

// ShopProduct is one visible product by slug; a draft, archived or unknown
// slug is 404 (BR-080).
func (s *Service) ShopProduct(ctx context.Context, slug string) (ShopProductDetail, error) {
	var d ShopProductDetail
	err := s.tx(ctx, func(q *sqlcgen.Queries, _ pgx.Tx) error {
		var err error
		d.StorefrontProductBySlugRow, err = q.StorefrontProductBySlug(ctx, slug)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.NotFound("No such product.")
		}
		if err != nil {
			return err
		}
		if d.Categories, err = q.StorefrontProductCategories(ctx, d.ID); err != nil {
			return err
		}
		if d.Variants, err = q.StorefrontVariantsOfProduct(ctx, d.ID); err != nil {
			return err
		}
		images, err := q.StorefrontProductImages(ctx, d.ID)
		for _, m := range images {
			d.Images = append(d.Images, s.shopImage(m.VariantID, m.R2Key, m.Derivatives, "1600"))
		}
		return err
	})
	return d, err
}

// ShopCategories is every unarchived category, or one kind's, by path.
func (s *Service) ShopCategories(ctx context.Context, kind *string) ([]sqlcgen.StorefrontCategoriesRow, error) {
	var rows []sqlcgen.StorefrontCategoriesRow
	err := s.tx(ctx, func(q *sqlcgen.Queries, _ pgx.Tx) (err error) {
		rows, err = q.StorefrontCategories(ctx, kind)
		return err
	})
	return rows, err
}

// ShopBrands is every unarchived brand, by name.
func (s *Service) ShopBrands(ctx context.Context) ([]sqlcgen.StorefrontBrandsRow, error) {
	var rows []sqlcgen.StorefrontBrandsRow
	err := s.tx(ctx, func(q *sqlcgen.Queries, _ pgx.Tx) (err error) {
		rows, err = q.StorefrontBrands(ctx)
		return err
	})
	return rows, err
}

// shopImage is an image's public URLs: the main one is the preferred width,
// or the original until the worker has made the renditions (BR-052).
func (s *Service) shopImage(variantID *uuid.UUID, key string, derivatives []byte, preferred string) ShopImage {
	img := ShopImage{VariantID: variantID, URL: s.files.PublicURL(key), Sizes: map[string]string{}}
	for size, k := range derivativeKeys(derivatives) {
		img.Sizes[size] = s.files.PublicURL(k)
	}
	if u, ok := img.Sizes[preferred]; ok {
		img.URL = u
	}
	return img
}
