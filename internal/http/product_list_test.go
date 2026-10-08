package httpapi_test

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/platform/config"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

func init() {
	isolationCases = append(isolationCases,
		isolationCase{route: route{"GET", "/v1/products"}, seed: seedProduct,
			request: func(t *testing.T, s seeded) *http.Request {
				return bearerRequest(t, http.MethodGet, "/v1/products?limit=200&status=draft", s.accessToken)
			}})
}

// TestProductList is P1-030's behaviour: filters, main-tree categories per
// row, descendants for category_id, exact SKU, prices, cursor paging.
func TestProductList(t *testing.T) {
	ctx := t.Context()
	store := openAppStore(ctx, t)
	tenantID := uuid.Must(uuid.NewV7())
	admin := seedSignedInUserWithRole(ctx, t, store, tenantID, auth.RoleAdmin)
	do := apiClient(t, admin)

	_, brand := do("POST", "/v1/brands", map[string]any{"name": "Erigo"})
	apparel := apiCreate(t, admin, "/v1/categories", map[string]any{"name": "Apparel"})
	tees := apiCreate(t, admin, "/v1/categories", map[string]any{"name": "Tees", "parent_id": apparel})
	series := apiCreate(t, admin, "/v1/categories", map[string]any{"name": "SS26", "kind": "series"})

	tee := apiCreate(t, admin, "/v1/products", map[string]any{"title": "Basic Tee", "brand_id": brand["id"],
		"category_ids": []string{tees, series}})
	apiCreate(t, admin, "/v1/products", map[string]any{"title": "Cargo Pants"})
	hoodie := apiCreate(t, admin, "/v1/products", map[string]any{"title": "Zip Hoodie"})
	for _, v := range []map[string]any{
		{"option_values": []string{}, "sku": "TEE-1", "regular_price": 19900000},
	} {
		apiCreate(t, admin, "/v1/products/"+tee+"/variants", v)
	}
	if err := store.InTenantTx(tenant.NewContext(ctx, tenantID), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE products SET status = 'active' WHERE id = $1`, hoodie)
		return err
	}); err != nil {
		t.Fatalf("activate: %v", err)
	}

	titles := func(path string) []string {
		t.Helper()
		code, p := do("GET", path, nil)
		if code != 200 {
			t.Fatalf("%s: %d %v", path, code, p)
		}
		var out []string
		for _, d := range p["data"].([]any) {
			out = append(out, d.(map[string]any)["title"].(string))
		}
		return out
	}
	for path, want := range map[string][]string{
		"/v1/products?sort=title":                                  {"Basic Tee", "Cargo Pants", "Zip Hoodie"},
		"/v1/products?sort=title&status=active":                    {"Zip Hoodie"},
		"/v1/products?sort=title&brand_id=" + brand["id"].(string): {"Basic Tee"},
		"/v1/products?sort=title&category_id=" + apparel:           {"Basic Tee"},
		"/v1/products?sort=title&q=hood":                           {"Zip Hoodie"},
		"/v1/products?sort=title&q=TEE-1":                          {"Basic Tee"},
		"/v1/products?status=archived":                             nil,
	} {
		if got := titles(path); !slices.Equal(got, want) {
			t.Errorf("%s: %v, want %v", path, got, want)
		}
	}

	t.Run("a row carries main-tree categories and live prices", func(t *testing.T) {
		_, p := do("GET", "/v1/products?q=Basic", nil)
		row := p["data"].([]any)[0].(map[string]any)
		cats := row["categories"].([]any)
		if len(cats) != 1 || cats[0].(map[string]any)["path"] != "apparel.tees" {
			t.Errorf("categories %v", cats)
		}
		if row["price_min"] != float64(19900000) || row["price_max"] != float64(19900000) || row["variant_count"] != float64(1) {
			t.Errorf("aggregates %v", row)
		}
		if row["brand"].(map[string]any)["name"] != "Erigo" || row["cover_url"] != nil {
			t.Errorf("brand/cover %v", row)
		}
	})
	t.Run("cursor pages cover every row once", func(t *testing.T) {
		for _, sort := range []string{"title", "-created_at", "-updated_at"} {
			var seen []string
			path := "/v1/products?limit=1&sort=" + sort
			for i := 0; path != "" && i < 10; i++ {
				_, p := do("GET", path, nil)
				for _, d := range p["data"].([]any) {
					seen = append(seen, d.(map[string]any)["id"].(string))
				}
				path = ""
				if c, ok := p["next_cursor"].(string); ok {
					path = "/v1/products?limit=1&sort=" + sort + "&cursor=" + c
				}
			}
			if len(seen) != 3 || len(slices.Compact(slices.Sorted(slices.Values(seen)))) != 3 {
				t.Errorf("sort %s: %v", sort, seen)
			}
		}
	})
	t.Run("bad sort is 422", func(t *testing.T) {
		code, p := do("GET", "/v1/products?sort=price", nil)
		assertProblem(t, code, p, 422, "validation_failed", "sort")
	})
}

// TestProductListP95 is P1-030's acceptance: p95 under 600 ms with 10,000
// products, categories included.
func TestProductListP95(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds 10k products")
	}
	ctx := t.Context()
	store := openAppStore(ctx, t)
	tenantID := uuid.Must(uuid.NewV7())
	admin := seedSignedInUserWithRole(ctx, t, store, tenantID, auth.RoleAdmin)
	tctx := tenant.NewContext(ctx, tenantID)

	root := uuid.Must(uuid.NewV7())
	if err := store.InTenantTx(tctx, func(tx pgx.Tx) error {
		for _, sql := range []string{
			`INSERT INTO categories (id, tenant_id, name) VALUES ('` + root.String() + `', current_setting('app.tenant_id')::uuid, 'Apparel')`,
			`INSERT INTO categories (id, tenant_id, parent_id, name)
			   SELECT gen_random_uuid(), current_setting('app.tenant_id')::uuid, '` + root.String() + `', 'Cat ' || g
			     FROM generate_series(1, 50) g`,
			`INSERT INTO products (id, tenant_id, title, slug, status)
			   SELECT gen_random_uuid(), current_setting('app.tenant_id')::uuid, 'Product ' || g || ' tee', 'p-' || g,
			          CASE WHEN g % 3 = 0 THEN 'active' ELSE 'draft' END
			     FROM generate_series(1, 10000) g`,
			`INSERT INTO product_categories (tenant_id, product_id, category_id)
			   SELECT p.tenant_id, p.id, (SELECT id FROM categories c WHERE c.parent_id IS NOT NULL ORDER BY md5(c.id::text || p.id::text) LIMIT 1)
			     FROM products p`,
			`INSERT INTO variants (id, tenant_id, product_id, sku, option_values, regular_price_amount)
			   SELECT gen_random_uuid(), p.tenant_id, p.id, 'SKU-' || p.slug || '-' || s, ARRAY[s::text], 10000000 + s
			     FROM products p, generate_series(1, 2) s`,
		} {
			if _, err := tx.Exec(ctx, sql); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed 10k: %v", err)
	}
	t.Cleanup(func() {
		bg := tenant.NewContext(t.Context(), tenantID)
		_ = store.InTenantTx(bg, func(tx pgx.Tx) error {
			_, err := tx.Exec(bg, `DELETE FROM products; DELETE FROM categories`)
			return err
		})
	})

	// Statistics as autovacuum would have them in production. ANALYZE needs
	// the table owner; app_user's attempt is silently skipped, which leaves the
	// planner believing the tables are empty.
	owner, err := pgx.Connect(ctx, config.DatabaseURL())
	if err != nil {
		t.Fatalf("connect as owner: %v", err)
	}
	defer func() { _ = owner.Close(ctx) }()
	if _, err := owner.Exec(ctx, `ANALYZE products; ANALYZE variants; ANALYZE product_categories; ANALYZE categories`); err != nil {
		t.Fatalf("analyze: %v", err)
	}

	srv := newServer(t)
	queries := []string{
		"/v1/products", "/v1/products?sort=title", "/v1/products?sort=-updated_at&status=active",
		"/v1/products?category_id=" + root.String(), "/v1/products?q=tee", "/v1/products?q=SKU-p-5000-1",
		"/v1/products?limit=200", "/v1/products?status=draft&sort=title",
	}
	var took []time.Duration
	slowest := map[string]time.Duration{}
	for i := 0; i < 40; i++ {
		start := time.Now()
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, bearerRequest(t, http.MethodGet, queries[i%len(queries)], admin.accessToken))
		took = append(took, time.Since(start))
		slowest[queries[i%len(queries)]] = max(slowest[queries[i%len(queries)]], time.Since(start))
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", queries[i%len(queries)], rec.Code, rec.Body)
		}
	}
	slices.Sort(took)
	p95 := took[len(took)*95/100]
	t.Logf("p95 %s over %d requests at 10k products; slowest per query: %v", p95, len(took), slowest)
	if p95 > 600*time.Millisecond {
		t.Errorf("p95 %s, want under 600ms", p95)
	}
}
