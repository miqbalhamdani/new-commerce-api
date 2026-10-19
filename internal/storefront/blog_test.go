package storefront

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

// TestStorefrontBlog is P1-605's acceptance (04-api-spec.md §11.11).
func TestStorefrontBlog(t *testing.T) {
	store := openStore(t)
	tenantA, keyA := seedKey(t, store, "https://a.example")
	tenantB, keyB := seedKey(t, store, "https://b.example")
	seedCatalog(t, store, tenantA)
	seedBlog(t, store, tenantA)
	execTenant(t, store, tenantB,
		`INSERT INTO blog_posts (id, tenant_id, title, slug, status, published_at)
		   VALUES (gen_random_uuid(), `+tid+`, 'Secret', 'secret-post', 'published', now())`)
	t.Cleanup(func() { cleanBlog(t, store, tenantB) })
	a := newShop(t, store, keyA)

	t.Run("categories: live ones, by position", func(t *testing.T) {
		code, body, raw := a.get(t, "/v1/storefront/blog/categories")
		if code != 200 || !slices.Equal(slugs(body), []string{"bedding-tips", "materials"}) {
			t.Errorf("categories: %d %s", code, raw)
		}
	})

	t.Run("live posts only, newest first, with what a card needs", func(t *testing.T) {
		code, body, raw := a.get(t, "/v1/storefront/blog/posts")
		if code != 200 || !slices.Equal(slugs(body), []string{"panduan", "sleepwear", "katun", "in-old"}) || body["total"] != 4.0 {
			t.Fatalf("list: %d %s", code, raw)
		}
		if strings.Contains(raw, "@example.com") || strings.Contains(raw, `"role"`) {
			t.Errorf("an author's email or role leaked (BR-133): %s", raw)
		}
		posts := body["data"].([]any)
		p := posts[0].(map[string]any)
		cover, _ := p["cover"].(map[string]any)
		if p["featured"] != true || p["excerpt"] != "Seprai dan selimut merupakan sarana pelengkap." ||
			p["cover_alt"] != p["title"] || cover == nil || !strings.HasSuffix(cover["url"].(string), "/shop/blogs/p/cover.jpg") ||
			len(cover["sizes"].(map[string]any)) != 0 {
			t.Errorf("panduan card: %v", p)
		}
		if author, _ := p["author"].(map[string]any); author == nil || author["name"] != "Yusuf" || author["bio"] != "Writes the journal." {
			t.Errorf("author: %v", p["author"])
		}
		if c, _ := p["category"].(map[string]any); c == nil || c["slug"] != "bedding-tips" || c["name"] != "Bedding tips" {
			t.Errorf("category: %v", p["category"])
		}
		if k := posts[2].(map[string]any); k["excerpt"] != "Written." || k["cover"] != nil || k["author"] != nil {
			t.Errorf("katun: a written excerpt, no cover, no author: %v", k)
		}
		if old := posts[3].(map[string]any); old["category"] != nil {
			t.Errorf("an archived category reads as none (BR-130): %v", old["category"])
		}
	})

	t.Run("featured, category, tag and pages", func(t *testing.T) {
		for path, want := range map[string][]string{
			"/v1/storefront/blog/posts?featured=true":                 {"panduan"},
			"/v1/storefront/blog/posts?featured=false":                {"sleepwear", "katun", "in-old"},
			"/v1/storefront/blog/posts?category=materials":            {"sleepwear", "katun"},
			"/v1/storefront/blog/posts?tag=Materials":                 {"sleepwear"},
			"/v1/storefront/blog/posts?category=old":                  {},
			"/v1/storefront/blog/posts?page=2&limit=3":                {"in-old"},
			"/v1/storefront/blog/posts?page=3&limit=3":                {},
			"/v1/storefront/blog/posts?featured=false&page=1&limit=2": {"sleepwear", "katun"},
		} {
			code, body, raw := a.get(t, path)
			if code != 200 || !slices.Equal(slugs(body), want) {
				t.Errorf("%s: %d %s, want %v", path, code, raw, want)
			}
		}
		_, body, _ := a.get(t, "/v1/storefront/blog/posts?featured=false&page=1&limit=2")
		if body["total"] != 3.0 {
			t.Errorf("total counts every page: %v", body["total"])
		}

		var got []string
		path := "/v1/storefront/blog/posts?limit=3"
		for pages := 0; path != ""; pages++ {
			code, body, raw := a.get(t, path)
			if code != 200 || pages > 3 {
				t.Fatalf("cursor page: %d %s", code, raw)
			}
			got = append(got, slugs(body)...)
			path = ""
			if c, ok := body["next_cursor"].(string); ok {
				path = "/v1/storefront/blog/posts?limit=3&cursor=" + c
			}
		}
		if !slices.Equal(got, []string{"panduan", "sleepwear", "katun", "in-old"}) {
			t.Errorf("cursor paging: %v", got)
		}
		code, body, _ := a.get(t, "/v1/storefront/blog/posts?page=1&cursor=abc")
		if code != 422 || !strings.Contains(fmtErrors(body), "page") {
			t.Errorf("page and cursor: %d %v", code, body)
		}
	})

	t.Run("a post: body, its visible products in order, related", func(t *testing.T) {
		code, body, raw := a.get(t, "/v1/storefront/blog/posts/panduan")
		if code != 200 || body["body"] != "# Panduan\n\nSeprai dan selimut merupakan sarana pelengkap.\n\n## 1. Anggaran" {
			t.Fatalf("detail: %d %s", code, raw)
		}
		var products []string
		for _, p := range body["products"].([]any) {
			products = append(products, p.(map[string]any)["slug"].(string))
		}
		if !slices.Equal(products, []string{"hoodie", "basic-tee"}) {
			t.Errorf("products %v: the visible ones in the post's order (BR-134)", products)
		}
		if len(body["related"].([]any)) != 0 {
			t.Errorf("panduan is alone in its category: %v", body["related"])
		}
		_, body, _ = a.get(t, "/v1/storefront/blog/posts/sleepwear")
		if rel := relatedSlugs(body); !slices.Equal(rel, []string{"katun"}) {
			t.Errorf("sleepwear related %v, want its category's", rel)
		}
		_, body, _ = a.get(t, "/v1/storefront/blog/posts/in-old")
		if rel := relatedSlugs(body); !slices.Equal(rel, []string{"panduan", "sleepwear", "katun"}) {
			t.Errorf("in-old related %v, want the newest of every post", rel)
		}
		for _, slug := range []string{"draft-post", "future-post", "archived-post", "secret-post", "nope"} {
			if code, _, raw := a.get(t, "/v1/storefront/blog/posts/"+slug); code != 404 {
				t.Errorf("%s: %d %s, want 404", slug, code, raw)
			}
		}
	})

	t.Run("each key sees its own shop", func(t *testing.T) {
		code, body, raw := newShop(t, store, keyB).get(t, "/v1/storefront/blog/posts")
		if code != 200 || !slices.Equal(slugs(body), []string{"secret-post"}) || strings.Contains(raw, "panduan") {
			t.Errorf("tenant B: %d %s", code, raw)
		}
	})
}

// seedBlog gives a shop two live categories and an archived one, four live
// posts, and three it must never show: a draft, a scheduled and an archived
// post. panduan links hoodie, the draft tee and basic-tee, in that order.
func seedBlog(t *testing.T, store *db.Store, tenantID uuid.UUID) {
	t.Helper()
	execTenant(t, store, tenantID,
		`INSERT INTO users (id, tenant_id, email, name, role, status, bio)
		   VALUES ('{idf001}', `+tid+`, 'yusuf-{idf001}@example.com', 'Yusuf', 'admin', 'active', 'Writes the journal.')`,
		`INSERT INTO blog_categories (id, tenant_id, name, slug, position) VALUES
		   ('{idc001}', `+tid+`, 'Bedding tips', 'bedding-tips', 1),
		   ('{idc002}', `+tid+`, 'Materials', 'materials', 2)`,
		`INSERT INTO blog_categories (id, tenant_id, name, slug, position, archived_at)
		   VALUES ('{idc003}', `+tid+`, 'Old', 'old', 0, now())`,
		`INSERT INTO blog_posts (id, tenant_id, category_id, author_id, title, slug, body, cover_r2_key, tags,
		                         status, is_featured, published_at)
		   VALUES ('{ide001}', `+tid+`, '{idc001}', '{idf001}', '7 Panduan', 'panduan',
		           E'# Panduan\n\nSeprai dan selimut merupakan sarana pelengkap.\n\n## 1. Anggaran', 'shop/blogs/p/cover.jpg',
		           '{"Bedding Tips"}', 'published', true, now() - interval '1 day')`,
		`INSERT INTO blog_posts (id, tenant_id, category_id, title, slug, tags, status, published_at)
		   VALUES (gen_random_uuid(), `+tid+`, '{idc002}', 'Sleepwear', 'sleepwear', '{"Materials"}', 'published', now() - interval '2 days')`,
		`INSERT INTO blog_posts (id, tenant_id, category_id, title, slug, excerpt, status, published_at)
		   VALUES (gen_random_uuid(), `+tid+`, '{idc002}', 'Katun', 'katun', 'Written.', 'published', now() - interval '3 days')`,
		`INSERT INTO blog_posts (id, tenant_id, category_id, title, slug, status, published_at)
		   VALUES (gen_random_uuid(), `+tid+`, '{idc003}', 'In Old', 'in-old', 'published', now() - interval '4 days')`,
		`INSERT INTO blog_posts (id, tenant_id, title, slug, status) VALUES (gen_random_uuid(), `+tid+`, 'Draft', 'draft-post', 'draft')`,
		`INSERT INTO blog_posts (id, tenant_id, title, slug, status, published_at)
		   VALUES (gen_random_uuid(), `+tid+`, 'Future', 'future-post', 'published', now() + interval '1 day')`,
		`INSERT INTO blog_posts (id, tenant_id, title, slug, status, published_at, archived_at)
		   VALUES (gen_random_uuid(), `+tid+`, 'Archived', 'archived-post', 'published', now() - interval '1 hour', now())`,
		`INSERT INTO blog_post_products (tenant_id, post_id, product_id, position)
		   SELECT `+tid+`, '{ide001}', id, CASE slug WHEN 'hoodie' THEN 0 WHEN 'draft-tee' THEN 1 ELSE 2 END
		   FROM products WHERE slug IN ('hoodie', 'draft-tee', 'basic-tee')`,
	)
	t.Cleanup(func() { cleanBlog(t, store, tenantID) })
}

// cleanBlog deletes a shop's blog and its staff, ahead of the catalog and
// key cleanups that delete the rest.
func cleanBlog(t *testing.T, store *db.Store, tenantID uuid.UUID) {
	ctx := context.WithoutCancel(t.Context())
	_ = store.InTenantTx(tenant.NewContext(ctx, tenantID), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM blog_post_products; DELETE FROM blog_posts; DELETE FROM blog_categories;
			DELETE FROM users`)
		return err
	})
}

func relatedSlugs(body map[string]any) []string {
	out := []string{}
	rel, _ := body["related"].([]any)
	for _, p := range rel {
		out = append(out, p.(map[string]any)["slug"].(string))
	}
	return out
}

func fmtErrors(body map[string]any) string {
	var fields []string
	errs, _ := body["errors"].([]any)
	for _, e := range errs {
		fields = append(fields, e.(map[string]any)["field"].(string))
	}
	return strings.Join(fields, ",")
}
