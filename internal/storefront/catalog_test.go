package storefront

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/carts"
	"github.com/miqbalhamdani/new-commerce-api/internal/catalog"
	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/platform/config"
	"github.com/miqbalhamdani/new-commerce-api/internal/storage"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

// shop is a storefront router plus a client for one key.
type shop struct {
	h   http.Handler
	key string
}

func newShop(t *testing.T, store *db.Store, key string) shop {
	t.Helper()
	files, err := storage.FromEnv()
	if err != nil {
		t.Fatalf("object store: %v", err)
	}
	return shop{h: NewRouter(NewServer(Services{Catalog: catalog.NewService(store, files, nil),
		Carts: carts.NewService(store, files)}), NewKeys(store)), key: key}
}

// get sends a GET with the shop's key and decodes the JSON answer.
func (s shop) get(t *testing.T, path string) (int, map[string]any, string) {
	t.Helper()
	return s.do(t, http.MethodGet, path, nil)
}

// do sends a request with the shop's key and, if body is not nil, body as
// JSON (a string is sent as it is).
func (s shop) do(t *testing.T, method, path string, body any, headers ...string) (int, map[string]any, string) {
	t.Helper()
	var reader io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		reader = strings.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	r := httptest.NewRequest(method, path, reader)
	r.Header.Set("X-Api-Key", s.key)
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	s.h.ServeHTTP(rec, r)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out, rec.Body.String()
}

// execTenant runs sql in tenantID. A {idXXXX} placeholder becomes a fresh
// uuid, the same one everywhere it appears in this call, so fixtures can
// refer to each other without colliding with an earlier run's rows.
func execTenant(t *testing.T, store *db.Store, tenantID uuid.UUID, sql ...string) {
	t.Helper()
	ctx := t.Context()
	ids := map[string]string{}
	placeholder := regexp.MustCompile(`\{id[0-9a-f]{4}\}`)
	err := store.InTenantTx(tenant.NewContext(ctx, tenantID), func(tx pgx.Tx) error {
		for _, q := range sql {
			q = placeholder.ReplaceAllStringFunc(q, func(m string) string {
				if ids[m] == "" {
					ids[m] = uuid.Must(uuid.NewV7()).String() // time-ordered, as the app's are
				}
				return ids[m]
			})
			if _, err := tx.Exec(ctx, q); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
}

const tid = "current_setting('app.tenant_id')::uuid"

// seedCatalog gives a shop one visible product with a sale (basic-tee), one
// with no brand or category (hoodie), and three it must never show: a draft,
// an archived product, and an archived variant of basic-tee.
func seedCatalog(t *testing.T, store *db.Store, tenantID uuid.UUID) {
	t.Helper()
	execTenant(t, store, tenantID,
		`INSERT INTO brands (id, tenant_id, name, slug) VALUES (gen_random_uuid(), `+tid+`, 'Erigo', 'erigo')`,
		`INSERT INTO brands (id, tenant_id, name, slug, archived_at) VALUES (gen_random_uuid(), `+tid+`, 'Old Brand', 'old', now())`,
		`INSERT INTO categories (id, tenant_id, name) VALUES ('{idc001}', `+tid+`, 'Apparel')`,
		`INSERT INTO categories (id, tenant_id, parent_id, name) VALUES (gen_random_uuid(), `+tid+`, '{idc001}', 'Tees')`,
		`INSERT INTO products (id, tenant_id, title, slug, status, brand_id, attributes, option_names)
		   VALUES ('{ida001}', `+tid+`, 'Basic Tee', 'basic-tee', 'active',
		           (SELECT id FROM brands WHERE slug = 'erigo'), '{"material":"Cotton Combed 30s"}', '{"Colour","Size"}')`,
		`INSERT INTO product_categories (tenant_id, product_id, category_id)
		   SELECT `+tid+`, '{ida001}', id FROM categories WHERE name = 'Tees'`,
		`INSERT INTO variants (id, tenant_id, product_id, sku, option_values, regular_price_amount, sale_price_amount)
		   VALUES ('{idb001}', `+tid+`, '{ida001}', 'TS-BLK-S', '{"Black","S"}', 19900000, 14900000)`,
		`INSERT INTO variants (id, tenant_id, product_id, sku, option_values, regular_price_amount)
		   VALUES ('{idb002}', `+tid+`, '{ida001}', 'TS-BLK-M', '{"Black","M"}', 21900000)`,
		`INSERT INTO variants (id, tenant_id, product_id, sku, option_values, regular_price_amount, archived_at)
		   VALUES ('{idb003}', `+tid+`, '{ida001}', 'TS-GONE', '{"White","S"}', 99900000, now())`,
		`INSERT INTO product_media (id, tenant_id, product_id, variant_id, r2_key, mime_type, bytes, position, derivatives)
		   VALUES (gen_random_uuid(), `+tid+`, '{ida001}', '{idb003}',
		           'shop/products/tee.jpg', 'image/jpeg', 1, 0, '{"1600":"shop/products/tee-1600.webp","800":"shop/products/tee-800.webp","200":"shop/products/tee-200.webp"}')`,
		`INSERT INTO products (id, tenant_id, title, slug, status) VALUES ('{ida002}', `+tid+`, 'Hoodie', 'hoodie', 'active')`,
		`INSERT INTO variants (id, tenant_id, product_id, option_values, regular_price_amount)
		   VALUES (gen_random_uuid(), `+tid+`, '{ida002}', '{}', 30000000)`,
		`INSERT INTO products (id, tenant_id, title, slug, status) VALUES (gen_random_uuid(), `+tid+`, 'Draft Tee', 'draft-tee', 'draft')`,
		`INSERT INTO variants (id, tenant_id, product_id, option_values, regular_price_amount)
		   SELECT gen_random_uuid(), `+tid+`, id, '{}', 100 FROM products WHERE slug = 'draft-tee'`,
		`INSERT INTO products (id, tenant_id, title, slug, status, archived_at) VALUES (gen_random_uuid(), `+tid+`, 'Gone Tee', 'gone-tee', 'active', now())`,
		`INSERT INTO variants (id, tenant_id, product_id, option_values, regular_price_amount)
		   SELECT gen_random_uuid(), `+tid+`, id, '{}', 100 FROM products WHERE slug = 'gone-tee'`,
	)
	t.Cleanup(func() { cleanCatalog(t, store, tenantID) })
}

func cleanCatalog(t *testing.T, store *db.Store, tenantID uuid.UUID) {
	// Cleanups run after t.Context() is cancelled.
	ctx := context.WithoutCancel(t.Context())
	_ = store.InTenantTx(tenant.NewContext(ctx, tenantID), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM cart_items; DELETE FROM carts;
			DELETE FROM product_media; DELETE FROM product_categories; DELETE FROM variants;
			DELETE FROM products; DELETE FROM categories; DELETE FROM brands`)
		return err
	})
}

func slugs(body map[string]any) []string {
	var out []string
	for _, row := range body["data"].([]any) {
		out = append(out, row.(map[string]any)["slug"].(string))
	}
	return out
}

// TestStorefrontCatalog is P1-203's acceptance (04-api-spec.md §11.4; BR-046,
// BR-080, BR-081).
func TestStorefrontCatalog(t *testing.T) {
	store := openStore(t)
	tenantA, keyA := seedKey(t, store, "https://a.example")
	tenantB, keyB := seedKey(t, store, "https://b.example")
	seedCatalog(t, store, tenantA)
	execTenant(t, store, tenantB,
		`INSERT INTO products (id, tenant_id, title, slug, status) VALUES ('{idb001}', `+tid+`, 'Secret Tee', 'secret-tee', 'active')`,
		`INSERT INTO variants (id, tenant_id, product_id, option_values, regular_price_amount)
		   VALUES (gen_random_uuid(), `+tid+`, '{idb001}', '{}', 5000000)`)
	t.Cleanup(func() { cleanCatalog(t, store, tenantB) })
	a := newShop(t, store, keyA)

	t.Run("only visible products, priced as the shopper pays", func(t *testing.T) {
		code, body, raw := a.get(t, "/v1/storefront/products")
		if code != 200 || !slices.Equal(slugs(body), []string{"hoodie", "basic-tee"}) {
			t.Fatalf("list: %d %s", code, raw)
		}
		if strings.Contains(raw, "secret-tee") || strings.Contains(raw, "Secret") {
			t.Error("tenant B's product appears in tenant A's list")
		}
		tee := body["data"].([]any)[1].(map[string]any)
		if tee["price_min"] != float64(14900000) || tee["price_max"] != float64(21900000) {
			t.Errorf("prices %v-%v: want the sale price and the archived variant ignored", tee["price_min"], tee["price_max"])
		}
		if b := tee["brand"].(map[string]any); b["slug"] != "erigo" || b["name"] != "Erigo" {
			t.Errorf("brand %v", b)
		}
		img := tee["image"].(map[string]any)
		if !strings.HasSuffix(img["url"].(string), "tee-800.webp") || len(img["sizes"].(map[string]any)) != 3 {
			t.Errorf("image %v", img)
		}
		if hoodie := body["data"].([]any)[0].(map[string]any); hoodie["brand"] != nil || hoodie["image"] != nil {
			t.Errorf("hoodie %v: brand and image should be null", hoodie)
		}
	})

	t.Run("filters", func(t *testing.T) {
		for path, want := range map[string][]string{
			"/v1/storefront/products?category=apparel":      {"basic-tee"}, // a descendant's product
			"/v1/storefront/products?category=apparel.tees": {"basic-tee"},
			"/v1/storefront/products?brand=erigo":           {"basic-tee"},
			"/v1/storefront/products?brand=old":             nil,
			"/v1/storefront/products?q=HOOD":                {"hoodie"},
			"/v1/storefront/products?sort=price_min":        {"basic-tee", "hoodie"},
			"/v1/storefront/products?sort=-price_min":       {"hoodie", "basic-tee"},
			"/v1/storefront/products?sort=title":            {"basic-tee", "hoodie"},
		} {
			code, body, raw := a.get(t, path)
			if code != 200 || !slices.Equal(slugs(body), want) {
				t.Errorf("%s: %d %s, want %v", path, code, raw, want)
			}
		}
		for path, field := range map[string]string{
			"/v1/storefront/products?sort=cheapest":       "sort",
			"/v1/storefront/products?category=Apparel!":   "category",
			"/v1/storefront/products?cursor=not-a-cursor": "cursor",
			"/v1/storefront/categories?kind=aisle":        "kind",
		} {
			code, body, _ := a.get(t, path)
			if errs, _ := body["errors"].([]any); code != 422 || len(errs) == 0 || errs[0].(map[string]any)["field"] != field {
				t.Errorf("%s: %d %v, want 422 on %s", path, code, body, field)
			}
		}
	})

	t.Run("cursor pages through every sort", func(t *testing.T) {
		for _, sort := range []string{"-updated_at", "price_min", "-price_min", "title"} {
			var seen []string
			path := "/v1/storefront/products?limit=1&sort=" + sort
			for range 3 {
				code, body, raw := a.get(t, path)
				if code != 200 {
					t.Fatalf("%s: %d %s", path, code, raw)
				}
				seen = append(seen, slugs(body)...)
				next, _ := body["next_cursor"].(string)
				if next == "" {
					break
				}
				path = "/v1/storefront/products?limit=1&sort=" + sort + "&cursor=" + next
			}
			if len(seen) != 2 || seen[0] == seen[1] {
				t.Errorf("sort %s paged %v, want both products once", sort, seen)
			}
		}
	})

	t.Run("detail", func(t *testing.T) {
		code, p, raw := a.get(t, "/v1/storefront/products/basic-tee")
		if code != 200 || p["title"] != "Basic Tee" || p["attributes"].(map[string]any)["material"] != "Cotton Combed 30s" {
			t.Fatalf("detail: %d %s", code, raw)
		}
		variants := p["variants"].([]any)
		if len(variants) != 2 {
			t.Fatalf("variants %v: the archived one must not appear", variants)
		}
		sale := variants[0].(map[string]any)
		if sale["sku"] != "TS-BLK-S" || sale["price"] != float64(14900000) || sale["regular_price"] != float64(19900000) || sale["on_sale"] != true {
			t.Errorf("sale variant %v", sale)
		}
		if full := variants[1].(map[string]any); full["on_sale"] != false || full["price"] != float64(21900000) {
			t.Errorf("full-price variant %v", full)
		}
		img := p["images"].([]any)[0].(map[string]any)
		if img["variant_id"] != nil || !strings.HasSuffix(img["url"].(string), "tee-1600.webp") {
			t.Errorf("image %v: an archived variant's id must not escape", img)
		}
		if cats := p["categories"].([]any); len(cats) != 1 || cats[0].(map[string]any)["path"] != "apparel.tees" {
			t.Errorf("categories %v", cats)
		}
		for _, field := range []string{"tenant_id", "status", "barcode", "version", "archived_at", "TS-GONE"} {
			if strings.Contains(raw, field) {
				t.Errorf("detail carries %q, which is outside the storefront views (BR-081)", field)
			}
		}
		for _, slug := range []string{"draft-tee", "gone-tee", "secret-tee", "nothing"} {
			code, body, _ := a.get(t, "/v1/storefront/products/"+slug)
			if code != 404 {
				t.Errorf("%s: %d %v, want 404", slug, code, body)
			}
		}
	})

	t.Run("categories and brands", func(t *testing.T) {
		code, body, raw := a.get(t, "/v1/storefront/categories")
		var paths []string
		for _, c := range body["data"].([]any) {
			paths = append(paths, c.(map[string]any)["path"].(string))
		}
		if code != 200 || !slices.Equal(paths, []string{"apparel", "apparel.tees"}) {
			t.Errorf("categories: %d %s", code, raw)
		}
		if _, body, _ := a.get(t, "/v1/storefront/categories?kind=series"); len(body["data"].([]any)) != 0 {
			t.Errorf("series: %v", body)
		}
		code, body, raw = a.get(t, "/v1/storefront/brands")
		if code != 200 || len(body["data"].([]any)) != 1 || body["data"].([]any)[0].(map[string]any)["slug"] != "erigo" {
			t.Errorf("brands: %d %s, want only the unarchived one", code, raw)
		}
	})

	t.Run("each key sees its own shop", func(t *testing.T) {
		code, body, raw := newShop(t, store, keyB).get(t, "/v1/storefront/products")
		if code != 200 || !slices.Equal(slugs(body), []string{"secret-tee"}) || strings.Contains(raw, "basic-tee") {
			t.Errorf("tenant B: %d %s", code, raw)
		}
	})
}

// TestStorefrontCatalogP95 is P1-203's p95 under 300 ms, at 10,000 products
// with two variants each, a third of them visible.
func TestStorefrontCatalogP95(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds 10k products")
	}
	ctx := t.Context()
	store := openStore(t)
	tenantID, key := seedKey(t, store, "https://p95.example")
	execTenant(t, store, tenantID,
		`INSERT INTO categories (id, tenant_id, name) VALUES ('{idc101}', `+tid+`, 'Apparel')`,
		`INSERT INTO categories (id, tenant_id, parent_id, name)
		   SELECT gen_random_uuid(), `+tid+`, '{idc101}', 'Cat ' || g FROM generate_series(1, 50) g`,
		`INSERT INTO products (id, tenant_id, title, slug, status)
		   SELECT gen_random_uuid(), `+tid+`, 'Product ' || g || ' tee', 'p-' || g,
		          CASE WHEN g % 3 = 0 THEN 'active' ELSE 'draft' END
		     FROM generate_series(1, 10000) g`,
		`INSERT INTO product_categories (tenant_id, product_id, category_id)
		   SELECT p.tenant_id, p.id, (SELECT id FROM categories c WHERE c.parent_id IS NOT NULL ORDER BY md5(c.id::text || p.id::text) LIMIT 1)
		     FROM products p`,
		`INSERT INTO variants (id, tenant_id, product_id, sku, option_values, regular_price_amount)
		   SELECT gen_random_uuid(), p.tenant_id, p.id, 'SKU-' || p.slug || '-' || s, ARRAY[s::text], 10000000 + (hashtext(p.slug) % 1000) * 1000 + s
		     FROM products p, generate_series(1, 2) s`,
	)
	t.Cleanup(func() { cleanCatalog(t, store, tenantID) })

	owner, err := pgx.Connect(ctx, config.DatabaseURL())
	if err != nil {
		t.Fatalf("connect as owner: %v", err)
	}
	defer func() { _ = owner.Close(ctx) }()
	if _, err := owner.Exec(ctx, `ANALYZE products; ANALYZE variants; ANALYZE product_categories; ANALYZE categories`); err != nil {
		t.Fatalf("analyze: %v", err)
	}

	s := newShop(t, store, key)
	paths := []string{
		"/v1/storefront/products?limit=24",
		"/v1/storefront/products?limit=24&sort=price_min",
		"/v1/storefront/products?limit=24&sort=-price_min",
		"/v1/storefront/products?limit=24&sort=title",
		"/v1/storefront/products?limit=24&category=apparel",
		"/v1/storefront/products?limit=24&q=99",
		"/v1/storefront/products/p-3000",
	}
	var took []time.Duration
	for i := range 140 {
		path := paths[i%len(paths)]
		start := time.Now()
		code, _, raw := s.get(t, path)
		took = append(took, time.Since(start))
		if code != 200 {
			t.Fatalf("%s: %d %s", path, code, raw)
		}
	}
	slices.Sort(took)
	p95 := took[len(took)*95/100]
	t.Logf("p95 %s over %d requests at 10k products", p95, len(took))
	if p95 > 300*time.Millisecond {
		t.Errorf("p95 %s, want under 300ms (P1-203)", p95)
	}
}
