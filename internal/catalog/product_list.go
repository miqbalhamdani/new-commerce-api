package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/db/sqlcgen"
)

// ProductFilter is GET /v1/products (04-api-spec.md §7.1).
type ProductFilter struct {
	Status     *string
	BrandID    *uuid.UUID
	CategoryID *uuid.UUID
	Q          *string
	Sort       string // "-created_at" (default), "-updated_at" or "title"
	After      *ProductCursor
	Limit      int
}

// ProductCursor is the keyset position after a page: the sort column's value
// and the id that breaks ties.
type ProductCursor struct {
	At    *time.Time `json:"t,omitempty"`
	Title *string    `json:"s,omitempty"`
	ID    uuid.UUID  `json:"i"`
}

// ProductRow is one list row with its references and aggregates resolved.
type ProductRow struct {
	ID                 uuid.UUID
	Version            int
	Title, Slug        string
	Status             string
	BrandID            *uuid.UUID
	BrandName          *string
	Categories         []CategoryBrief
	VariantCount       int
	PriceMin, PriceMax *int64
	CoverURL           *string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

type CategoryBrief struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
	Path string    `json:"path"`
}

// ListProducts is the catalog list (P1-030). Hand-written rather than sqlc:
// the sort decides both ORDER BY and the keyset predicate, and three copies of
// one query would drift.
//
// q matches the title by trigram (gin_trgm_ops serves ILIKE) or a variant's
// SKU exactly; category_id includes its descendants; categories on each row
// are the main tree only, by path.
func (s *Service) ListProducts(ctx context.Context, f ProductFilter) ([]ProductRow, *ProductCursor, error) {
	var (
		where []string
		args  []any
	)
	arg := func(v any) string { args = append(args, v); return fmt.Sprintf("$%d", len(args)) }

	if f.Status != nil && *f.Status == "archived" {
		where = append(where, "p.archived_at IS NOT NULL")
	} else {
		where = append(where, "p.archived_at IS NULL")
		if f.Status != nil {
			where = append(where, "p.status = "+arg(*f.Status))
		}
	}
	if f.BrandID != nil {
		where = append(where, "p.brand_id = "+arg(*f.BrandID))
	}
	if f.CategoryID != nil {
		// The subtree's products, resolved once rather than per product.
		where = append(where, `p.id IN (SELECT pc.product_id FROM product_categories pc
			JOIN categories d ON d.id = pc.category_id
			JOIN categories base ON base.id = `+arg(*f.CategoryID)+`
			WHERE d.kind = base.kind AND d.path <@ base.path)`)
	}
	if q := likePattern(f.Q); q != nil {
		exact := arg(strings.TrimSpace(*f.Q))
		where = append(where, "(p.title ILIKE '%' || "+arg(*q)+" || '%' OR EXISTS (SELECT 1 FROM variants sv WHERE sv.product_id = p.id AND sv.sku = "+exact+"))")
	}

	order := "p.created_at DESC, p.id DESC"
	switch f.Sort {
	case "-updated_at":
		order = "p.updated_at DESC, p.id DESC"
		if f.After != nil && f.After.At != nil {
			where = append(where, "(p.updated_at, p.id) < ("+arg(*f.After.At)+", "+arg(f.After.ID)+")")
		}
	case "title":
		order = "p.title, p.id"
		if f.After != nil && f.After.Title != nil {
			where = append(where, "(p.title, p.id) > ("+arg(*f.After.Title)+", "+arg(f.After.ID)+")")
		}
	default:
		if f.After != nil && f.After.At != nil {
			where = append(where, "(p.created_at, p.id) < ("+arg(*f.After.At)+", "+arg(f.After.ID)+")")
		}
	}

	// The page is chosen first; the per-row aggregates then run for those
	// rows only, not for every row that matches the filter.
	sql := `WITH page AS (
	  SELECT p.* FROM products p
	   WHERE ` + strings.Join(where, " AND ") + `
	   ORDER BY ` + order + `
	   LIMIT ` + arg(f.Limit+1) + `)
	SELECT p.id, p.version, p.title, p.slug, p.status, p.brand_id, b.name, p.created_at, p.updated_at,
	  coalesce((SELECT json_agg(json_build_object('id', c.id, 'name', c.name, 'path', c.path::text) ORDER BY c.path)
	              FROM product_categories pc JOIN categories c ON c.id = pc.category_id
	             WHERE pc.product_id = p.id AND c.kind = 'category'), '[]'::json),
	  v.n, v.lo, v.hi, ` + coverURLSQL + `
	FROM page p
	LEFT JOIN brands b ON b.id = p.brand_id
	LEFT JOIN LATERAL (SELECT count(*)::int AS n, min(variant_price(x)) AS lo, max(variant_price(x)) AS hi
	                     FROM variants x WHERE x.product_id = p.id AND x.archived_at IS NULL) v ON true
	ORDER BY ` + order

	var out []ProductRow
	err := s.tx(ctx, func(_ *sqlcgen.Queries, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r ProductRow
			var cats []byte
			var cover *string
			if err := rows.Scan(&r.ID, &r.Version, &r.Title, &r.Slug, &r.Status, &r.BrandID, &r.BrandName,
				&r.CreatedAt, &r.UpdatedAt, &cats, &r.VariantCount, &r.PriceMin, &r.PriceMax, &cover); err != nil {
				return err
			}
			if err := json.Unmarshal(cats, &r.Categories); err != nil {
				return err
			}
			r.CoverURL = s.publicURL(cover)
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil || len(out) <= f.Limit {
		return out, nil, err
	}
	out = out[:f.Limit]
	last := out[f.Limit-1]
	next := &ProductCursor{ID: last.ID}
	switch f.Sort {
	case "title":
		next.Title = &last.Title
	case "-updated_at":
		next.At = &last.UpdatedAt
	default:
		next.At = &last.CreatedAt
	}
	return out, next, nil
}

// coverURLSQL selects the key of a product's cover thumbnail: the first
// image's 200 px derivative, once the worker has made it (BR-052).
const coverURLSQL = `(SELECT m.derivatives->>'200' FROM product_media m
	WHERE m.product_id = p.id ORDER BY m.position, m.id LIMIT 1)`

// publicURL turns a stored object key into a URL on the image domain (BR-050).
func (s *Service) publicURL(key *string) *string {
	if key == nil {
		return nil
	}
	u := s.files.PublicURL(*key)
	return &u
}
