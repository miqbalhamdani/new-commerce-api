package main

import (
	"crypto/rand"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/platform/config"
	"github.com/miqbalhamdani/new-commerce-api/internal/queue"
	"github.com/miqbalhamdani/new-commerce-api/internal/storage"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

func TestValidate(t *testing.T) {
	ok := shop{Name: "Toko Demo", Slug: "demo", OrderPrefix: "DEMO", OwnerEmail: "demo@belli.id", Theme: "clothing"}
	if err := ok.validate(); err != nil {
		t.Fatalf("valid shop: %v", err)
	}
	for name, mutate := range map[string]func(*shop){
		"lowercase prefix": func(s *shop) { s.OrderPrefix = "demo" },
		"slug with space":  func(s *shop) { s.Slug = "toko demo" },
		"no name":          func(s *shop) { s.Name = "" },
		"no email":         func(s *shop) { s.OwnerEmail = "" },
		"unknown theme":    func(s *shop) { s.Theme = "minimal" },
	} {
		s := ok
		mutate(&s)
		if s.validate() == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestMatrixAndCodes(t *testing.T) {
	now := time.Now()
	n := 0
	names, rows := matrix(products[1], &n, now) // Kaos Oversize: 2 colours x 3 sizes, on sale now
	if strings.Join(names, ",") != "Warna,Ukuran" || len(rows) != 6 {
		t.Fatalf("names %v, %d rows; want Warna,Ukuran and 6", names, len(rows))
	}
	f := rows[0].Fields
	if *f.SKU != "KOS-HIT-M" || *f.RegularPrice != 15900000 || *f.SalePrice != 13500000 || !f.SaleEndsAt.After(now) {
		t.Errorf("first row = sku %s price %d sale %d", *f.SKU, *f.RegularPrice, *f.SalePrice)
	}
	if got := ean13(1); got != "8990000000013" {
		t.Errorf("ean13(1) = %s", got)
	}
	_, rows = matrix(products[3], &n, now) // batik: XXL costs Rp10.000 more
	if last := rows[len(rows)-1].Fields; *last.RegularPrice != 35900000 {
		t.Errorf("XXL price = %d, want 35900000", *last.RegularPrice)
	}
}

// TestSeed seeds a whole demo shop against the test database, bucket and
// Redis, then checks every screen has what the demo promises -- and that not
// one email was queued.
func TestSeed(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds a whole shop")
	}
	ctx := t.Context()
	store, err := db.New(ctx, config.AppDatabaseURL())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(store.Close)
	files, err := storage.FromEnv()
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	q, err := queue.New(ctx, config.RedisURL())
	if err != nil {
		t.Fatalf("redis: %v", err)
	}
	t.Cleanup(func() { _ = q.Close() })
	opt, err := redis.ParseURL(config.RedisURL())
	if err != nil {
		t.Fatal(err)
	}
	rdb := redis.NewClient(opt)
	t.Cleanup(func() { _ = rdb.Close() })
	emailsBefore := rdb.XLen(ctx, "emails").Val()

	suffix := strings.ToLower(rand.Text()[:8])
	s := shop{Name: "Demo Seed Test", Slug: "demo-seed-" + suffix, OrderPrefix: "DST", OwnerEmail: "demo-" + suffix + "@example.com"}
	out, err := newSeeder(store, files, q).seed(ctx, s, "a-password-for-tests", time.Now())
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	owner, err := store.LookupUserForAuth(ctx, s.OwnerEmail)
	if err != nil || owner.TenantID != out.tenantID || owner.PasswordHash == nil {
		t.Fatalf("owner = %+v, %v", owner, err)
	}
	if err := auth.VerifyPassword(*owner.PasswordHash, "a-password-for-tests"); err != nil {
		t.Errorf("owner password: %v", err)
	}
	if len(out.logins) != 4 {
		t.Errorf("logins = %v, want the owner, admin, ops and viewer", out.logins)
	}

	tctx := tenant.NewContext(ctx, out.tenantID)
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := store.InTenantTx(tctx, func(tx pgx.Tx) error { return tx.QueryRow(ctx, query, args...).Scan(&n) }); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		return n
	}
	want := func(what string, got, min int) {
		t.Helper()
		if got < min {
			t.Errorf("%s = %d, want at least %d", what, got, min)
		}
	}

	byState := map[string]int{}
	for _, p := range products {
		byState[p.state]++
	}
	for state, n := range byState {
		if got := count(`SELECT count(*) FROM products WHERE status = $1`, state); got != n {
			t.Errorf("%s products = %d, want %d", state, got, n)
		}
	}
	for _, status := range []string{"pending", "paid", "processing", "shipped", "completed", "cancelled"} {
		want(status+" orders", count(`SELECT count(*) FROM orders WHERE status = $1`, status), 1)
	}
	want("refund owed", count(`SELECT count(*) FROM orders WHERE status = 'cancelled' AND paid_at IS NOT NULL AND refunded_at IS NULL`), 1)
	want("refunded", count(`SELECT count(*) FROM orders WHERE refunded_at IS NOT NULL`), 1)
	want("storefront orders", count(`SELECT count(*) FROM orders WHERE source = 'storefront' AND customer_id IS NOT NULL`), 1)
	want("manual orders", count(`SELECT count(*) FROM orders WHERE source = 'manual'`), 1)
	want("Midtrans orders", count(`SELECT count(*) FROM orders WHERE payment_method = 'midtrans'`), 1)
	want("orders placed two months ago", count(`SELECT count(*) FROM orders WHERE placed_at < now() - interval '60 days'`), 1)
	if n := count(`SELECT count(*) FROM orders WHERE customer->>'email' IS NOT NULL`); n != 0 {
		t.Errorf("%d orders carry a customer email; a demo click would mail it", n)
	}
	for _, status := range []string{"pending", "paid", "failed"} {
		want(status+" payment attempts", count(`SELECT count(*) FROM payments WHERE status = $1`, status), 1)
	}
	want("history by the former staff member", count(`SELECT count(*) FROM audit_log a JOIN users u ON u.id = a.actor_id
		WHERE a.subject_type = 'order' AND u.status = 'disabled'`), 1)

	for _, role := range []string{"owner", "admin", "ops", "viewer"} {
		want(role+" users", count(`SELECT count(*) FROM users WHERE role = $1`, role), 1)
	}
	for _, status := range []string{"active", "invited", "disabled"} {
		want(status+" users", count(`SELECT count(*) FROM users WHERE status = $1`, status), 1)
	}

	if got := count(`SELECT count(*) FROM product_media`); got != out.photos || got == 0 {
		t.Errorf("media = %d, want %d", got, out.photos)
	}
	if got := count(`SELECT count(*) FROM jobs WHERE kind = 'image_derivatives'`); got != out.photos {
		t.Errorf("derivative jobs = %d, want one per photo (%d)", got, out.photos)
	}
	if got := count(`SELECT count(*) FROM customers`); got != len(customers) {
		t.Errorf("customers = %d, want %d", got, len(customers))
	}
	want("Google sign-ins", count(`SELECT count(*) FROM customer_identities`), 1)
	if got := count(`SELECT count(*) FROM api_keys WHERE revoked_at IS NOT NULL`); got != 1 || count(`SELECT count(*) FROM api_keys`) != 2 {
		t.Errorf("api keys: want two, one revoked")
	}
	want("storefront settings", count(`SELECT count(*) FROM storefront_settings WHERE origin_postal_code = '40115'`), 1)
	want("archived brands", count(`SELECT count(*) FROM brands WHERE archived_at IS NOT NULL`), 1)
	want("archived categories", count(`SELECT count(*) FROM categories WHERE archived_at IS NOT NULL`), 1)
	want("archived variants of active products", count(`SELECT count(*) FROM variants v JOIN products p ON p.id = v.product_id
		WHERE v.archived_at IS NOT NULL AND p.status = 'active'`), 1)
	want("catalog set up before the first order", count(`SELECT count(*) FROM products WHERE created_at < now() - interval '90 days'`), 1)

	if got := rdb.XLen(ctx, "emails").Val(); got != emailsBefore {
		t.Errorf("emails stream grew from %d to %d", emailsBefore, got)
	}

	if _, err := newSeeder(store, files, q).seed(ctx, s, "x", time.Now()); err == nil || !strings.Contains(err.Error(), "insert shop") {
		t.Errorf("second run with the same slug: err = %v, want an insert shop error", err)
	}
}

// TestSeedThemes seeds a demo shop in two themes (P1-804; BR-143): the set's
// catalog with its prices, sales and drafts, its articles with their
// products, and the shop drawn in the theme. Photos are not fetched here, so
// each is a placeholder card and the hero and covers stay out.
func TestSeedThemes(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds whole shops")
	}
	ctx := t.Context()
	store, err := db.New(ctx, config.AppDatabaseURL())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(store.Close)
	files, err := storage.FromEnv()
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	q, err := queue.New(ctx, config.RedisURL())
	if err != nil {
		t.Fatalf("redis: %v", err)
	}
	t.Cleanup(func() { _ = q.Close() })

	for _, theme := range []string{"busana", "furnitur-ceria"} {
		t.Run(theme, func(t *testing.T) {
			look := looks[theme]
			set, err := loadSet(look.set, look)
			if err != nil {
				t.Fatal(err)
			}
			suffix := strings.ToLower(rand.Text()[:8])
			s := shop{Name: "Tema " + theme, Slug: "tema-" + suffix, OrderPrefix: "TM", OwnerEmail: "tema-" + suffix + "@example.com", Theme: theme}
			out, err := newSeeder(store, files, q).seed(ctx, s, "a-password-for-tests", time.Now())
			if err != nil {
				t.Fatalf("seed: %v", err)
			}
			tctx := tenant.NewContext(ctx, out.tenantID)
			count := func(query string, args ...any) int {
				t.Helper()
				var n int
				if err := store.InTenantTx(tctx, func(tx pgx.Tx) error { return tx.QueryRow(ctx, query, args...).Scan(&n) }); err != nil {
					t.Fatalf("%s: %v", query, err)
				}
				return n
			}
			drafts, photos := 0, 0
			for _, p := range set.products {
				if p.state == "draft" {
					drafts++
				}
				photos += len(p.photos)
			}
			if got := count(`SELECT count(*) FROM products WHERE status = 'active'`); got != len(set.products)-drafts {
				t.Errorf("active products = %d, want %d", got, len(set.products)-drafts)
			}
			if got := count(`SELECT count(*) FROM products WHERE status = 'draft'`); got != drafts {
				t.Errorf("drafts = %d, want %d (the design's product without a photo)", got, drafts)
			}
			if out.photos != photos || count(`SELECT count(*) FROM product_media`) != photos {
				t.Errorf("photos = %d, want %d", out.photos, photos)
			}
			if got := count(`SELECT count(*) FROM categories WHERE kind = 'collection'`); got != 3 {
				t.Errorf("collections = %d, want the design's 3", got)
			}
			if got := count(`SELECT count(*) FROM variants WHERE sale_price_amount IS NOT NULL`); got == 0 {
				t.Error("no variant is on sale; the design crosses prices out")
			}
			if got := count(`SELECT count(*) FROM blog_posts WHERE status = 'published'`); got != len(set.posts) || out.posts != len(set.posts) {
				t.Errorf("published articles = %d, want %d", got, len(set.posts))
			}
			if got := count(`SELECT count(*) FROM blog_post_products`); got == 0 {
				t.Error("no article links its products")
			}
			if got := count(`SELECT count(*) FROM storefront_settings WHERE theme = $1 AND brand_color IS NULL
				AND whatsapp_number = $2 AND tagline = $3`, theme, whatsappBelli, look.tagline); got != 1 {
				t.Errorf("look: want theme %s, its first colour, Belli's WhatsApp and the tagline", theme)
			}
		})
	}
	// A value's surcharge lands on every variant it is in (furnitur: Sofa
	// Arunika, 3 dudukan +Rp1.500.000, Bouclé Putih +Rp350.000).
	n := 0
	_, rows := setMatrix(demoProduct{sku: "SA2", price: 6900000, weight: 1, options: []demoOption{
		{Name: "Ukuran", Values: []struct {
			Value string `json:"value"`
			Add   int64  `json:"add"`
		}{{"2 dudukan", 0}, {"3 dudukan", 1500000}}},
		{Name: "Kain", Values: []struct {
			Value string `json:"value"`
			Add   int64  `json:"add"`
		}{{"Linen Krem", 0}, {"Bouclé Putih", 350000}}},
	}}, &n)
	if len(rows) != 4 || *rows[3].Fields.RegularPrice != 875000000 || *rows[3].Fields.SKU != "SA2-04" {
		t.Errorf("last row = %v %d", rows[3].OptionValues, *rows[3].Fields.RegularPrice)
	}
}
