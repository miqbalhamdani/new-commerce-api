package httpapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

// Blog routes (P1-602, P1-603) in the isolation and audit suites. Tenant A
// acts on tenant B's category or post by id; B's name is the marker.
func init() {
	categoryByID := func(method string) func(t *testing.T, s seeded) *http.Request {
		return func(t *testing.T, s seeded) *http.Request {
			return bodyRequest(t, method, "/v1/blog/categories/"+s.otherID, s.accessToken, map[string]any{"position": 9})
		}
	}
	postByID := func(method string) func(t *testing.T, s seeded) *http.Request {
		return func(t *testing.T, s seeded) *http.Request {
			return bodyRequest(t, method, "/v1/blog/posts/"+s.otherID, s.accessToken, map[string]any{"body": "Edited."})
		}
	}
	isolationCases = append(isolationCases,
		isolationCase{route: route{"GET", "/v1/blog/categories"}, seed: seedBlogCategory,
			request: func(t *testing.T, s seeded) *http.Request {
				return bearerRequest(t, http.MethodGet, "/v1/blog/categories", s.accessToken)
			}},
		isolationCase{route: route{"POST", "/v1/blog/categories"}, seed: seedBlogCategory,
			request: func(t *testing.T, s seeded) *http.Request {
				return bodyRequest(t, http.MethodPost, "/v1/blog/categories", s.accessToken, map[string]string{"name": "New " + uuid.NewString()})
			}},
		isolationCase{route: route{"PATCH", "/v1/blog/categories/{id}"}, seed: seedBlogCategory, request: categoryByID(http.MethodPatch)},
		isolationCase{route: route{"DELETE", "/v1/blog/categories/{id}"}, seed: seedBlogCategory, request: categoryByID(http.MethodDelete)},
		isolationCase{route: route{"GET", "/v1/blog/posts"}, seed: seedBlogPost,
			request: func(t *testing.T, s seeded) *http.Request {
				return bearerRequest(t, http.MethodGet, "/v1/blog/posts?limit=200", s.accessToken)
			}},
		isolationCase{route: route{"POST", "/v1/blog/posts"}, seed: seedBlogPost,
			request: func(t *testing.T, s seeded) *http.Request {
				// Aimed at B's category: it must be 422, never B's name.
				return bodyRequest(t, http.MethodPost, "/v1/blog/posts", s.accessToken,
					map[string]any{"title": "New " + uuid.NewString(), "category_id": s.otherProduct})
			}},
		isolationCase{route: route{"GET", "/v1/blog/posts/{id}"}, seed: seedBlogPost, request: postByID(http.MethodGet)},
		isolationCase{route: route{"PATCH", "/v1/blog/posts/{id}"}, seed: seedBlogPost, request: postByID(http.MethodPatch)},
		isolationCase{route: route{"DELETE", "/v1/blog/posts/{id}"}, seed: seedBlogPost, request: postByID(http.MethodDelete)},
	)

	auditCases = append(auditCases,
		auditCase{route: route{"POST", "/v1/blog/categories"}, request: func(t *testing.T, s seeded) (*http.Request, string, string) {
			return bodyRequest(t, http.MethodPost, "/v1/blog/categories", s.accessToken, map[string]string{"name": "Audited " + uuid.NewString()}), "blog_category", ""
		}},
		auditCase{route: route{"PATCH", "/v1/blog/categories/{id}"}, request: func(t *testing.T, s seeded) (*http.Request, string, string) {
			id := apiCreate(t, s, "/v1/blog/categories", map[string]string{"name": "Before " + uuid.NewString()})
			return bodyRequest(t, http.MethodPatch, "/v1/blog/categories/"+id, s.accessToken, map[string]any{"position": 3}), "blog_category", id
		}},
		auditCase{route: route{"DELETE", "/v1/blog/categories/{id}"}, request: func(t *testing.T, s seeded) (*http.Request, string, string) {
			id := apiCreate(t, s, "/v1/blog/categories", map[string]string{"name": "Doomed " + uuid.NewString()})
			return bearerRequest(t, http.MethodDelete, "/v1/blog/categories/"+id, s.accessToken), "blog_category", id
		}},
		auditCase{route: route{"POST", "/v1/blog/posts"}, request: func(t *testing.T, s seeded) (*http.Request, string, string) {
			return bodyRequest(t, http.MethodPost, "/v1/blog/posts", s.accessToken, map[string]string{"title": "Audited " + uuid.NewString()}), "blog_post", ""
		}},
		auditCase{route: route{"PATCH", "/v1/blog/posts/{id}"}, request: func(t *testing.T, s seeded) (*http.Request, string, string) {
			id := apiCreate(t, s, "/v1/blog/posts", map[string]string{"title": "Before " + uuid.NewString()})
			return bodyRequest(t, http.MethodPatch, "/v1/blog/posts/"+id, s.accessToken, map[string]any{"status": "published"}), "blog_post", id
		}},
		auditCase{route: route{"DELETE", "/v1/blog/posts/{id}"}, request: func(t *testing.T, s seeded) (*http.Request, string, string) {
			id := apiCreate(t, s, "/v1/blog/posts", map[string]string{"title": "Doomed " + uuid.NewString()})
			return bearerRequest(t, http.MethodDelete, "/v1/blog/posts/"+id, s.accessToken), "blog_post", id
		}},
	)
}

// seedBlogCategory is a signed-in admin plus one blog category whose name is
// the marker.
func seedBlogCategory(ctx context.Context, t *testing.T, store *db.Store, tenantID uuid.UUID) seeded {
	t.Helper()
	s := seedSignedInUserWithRole(ctx, t, store, tenantID, auth.RoleAdmin)
	id := uuid.Must(uuid.NewV7())
	s.marker = "Category " + id.String()
	s.id = id.String()
	if err := store.InTenantTx(tenant.NewContext(ctx, tenantID), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO blog_categories (id, tenant_id, name, slug) VALUES ($1, $2, $3, slugify($3))`,
			id, tenantID, s.marker)
		return err
	}); err != nil {
		t.Fatalf("seed blog category: %v", err)
	}
	return s
}

// seedBlogPost is a signed-in admin plus a category and a published post in
// it; the post's title and the category's name carry the marker. product is
// the category, for a request that aims at the other tenant's.
func seedBlogPost(ctx context.Context, t *testing.T, store *db.Store, tenantID uuid.UUID) seeded {
	t.Helper()
	s := seedBlogCategory(ctx, t, store, tenantID)
	s.product = s.id
	id := uuid.Must(uuid.NewV7())
	s.id = id.String()
	if err := store.InTenantTx(tenant.NewContext(ctx, tenantID), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO blog_posts (id, tenant_id, category_id, title, slug, body, status, published_at)
			VALUES ($1, $2, $3, $4, slugify($4), $4, 'published', now())`, id, tenantID, s.product, s.marker)
		return err
	}); err != nil {
		t.Fatalf("seed blog post: %v", err)
	}
	return s
}

// TestBlogCategories is P1-602's acceptance (04-api-spec.md §13.1).
func TestBlogCategories(t *testing.T) {
	ctx := t.Context()
	store := openAppStore(ctx, t)
	admin := seedSignedInUserWithRole(ctx, t, store, uuid.Must(uuid.NewV7()), auth.RoleAdmin)
	do := apiClient(t, admin)

	code, c := do("POST", "/v1/blog/categories", map[string]any{"name": "Bedding tips", "position": 1})
	if code != 201 || c["slug"] != "bedding-tips" || c["position"] != 1.0 || c["archived_at"] != nil {
		t.Fatalf("create: %d %v", code, c)
	}
	id := c["id"].(string)

	t.Run("a taken slug is 422 on slug when sent, else on name", func(t *testing.T) {
		code, p := do("POST", "/v1/blog/categories", map[string]any{"name": "Other", "slug": "bedding-tips"})
		assertProblem(t, code, p, 422, "validation_failed", "slug")
		code, p = do("POST", "/v1/blog/categories", map[string]any{"name": "bedding  TIPS"})
		assertProblem(t, code, p, 422, "validation_failed", "name")
		code, p = do("POST", "/v1/blog/categories", map[string]any{"name": "X", "slug": "Bad Slug"})
		assertProblem(t, code, p, 422, "validation_failed", "slug")
		code, p = do("POST", "/v1/blog/categories", map[string]any{"name": "X", "path": "x"})
		assertProblem(t, code, p, 422, "validation_failed", "path")
	})
	t.Run("a reorder keeps the slug; a rename re-derives it; a sent slug is kept", func(t *testing.T) {
		code, p := do("PATCH", "/v1/blog/categories/"+id, map[string]any{"position": 5})
		if code != 200 || p["slug"] != "bedding-tips" || p["position"] != 5.0 {
			t.Fatalf("reorder: %d %v", code, p)
		}
		code, p = do("PATCH", "/v1/blog/categories/"+id, map[string]any{"name": "Tips"})
		if code != 200 || p["slug"] != "tips" {
			t.Fatalf("rename: %d %v", code, p)
		}
		code, p = do("PATCH", "/v1/blog/categories/"+id, map[string]any{"name": "Tips & tricks", "slug": "tips"})
		if code != 200 || p["slug"] != "tips" || p["name"] != "Tips & tricks" {
			t.Fatalf("rename with slug: %d %v", code, p)
		}
	})
	t.Run("the list is by position then name; delete archives", func(t *testing.T) {
		for _, n := range []string{"Materials", "Community"} {
			if code, p := do("POST", "/v1/blog/categories", map[string]any{"name": n, "position": 2}); code != 201 {
				t.Fatalf("create %s: %d %v", n, code, p)
			}
		}
		code, p := do("GET", "/v1/blog/categories", nil)
		var names []string
		for _, d := range p["data"].([]any) {
			names = append(names, d.(map[string]any)["name"].(string))
		}
		if code != 200 || strings.Join(names, ",") != "Community,Materials,Tips & tricks" {
			t.Fatalf("list: %d %v", code, names)
		}
		if code, _ := do("DELETE", "/v1/blog/categories/"+id, nil); code != 204 {
			t.Fatalf("delete: %d", code)
		}
		_, p = do("GET", "/v1/blog/categories", nil)
		_, a := do("GET", "/v1/blog/categories?archived=true", nil)
		if len(p["data"].([]any)) != 2 || len(a["data"].([]any)) != 1 {
			t.Errorf("after delete: live %v, archived %v", p, a)
		}
	})
	t.Run("ops reads but cannot write", func(t *testing.T) {
		ops := seedSignedInUserWithRole(ctx, t, store, uuid.Must(uuid.NewV7()), auth.RoleOps)
		rec := httptest.NewRecorder()
		srv := newServer(t)
		srv.ServeHTTP(rec, bearerRequest(t, http.MethodGet, "/v1/blog/categories", ops.accessToken))
		if rec.Code != 200 {
			t.Errorf("ops list: %d", rec.Code)
		}
		rec = httptest.NewRecorder()
		srv.ServeHTTP(rec, bodyRequest(t, http.MethodPost, "/v1/blog/categories", ops.accessToken, map[string]string{"name": "Nope"}))
		if rec.Code != 403 || !strings.Contains(rec.Body.String(), "blog:write") {
			t.Errorf("ops create: %d %s", rec.Code, rec.Body)
		}
	})
}

// TestBlogPosts is P1-603's acceptance (04-api-spec.md §13.2, §8).
func TestBlogPosts(t *testing.T) {
	ctx := t.Context()
	store := openAppStore(ctx, t)
	tenantID := uuid.Must(uuid.NewV7())
	admin := seedSignedInUserWithRole(ctx, t, store, tenantID, auth.RoleAdmin)
	do := apiClient(t, admin)

	body := "# Panduan\n\nSeprai dan selimut merupakan sarana pelengkap istirahat Anda.\n\n## 1. Anggaran\n\nTentukan."
	code, p := do("POST", "/v1/blog/posts", map[string]any{"title": "7 Panduan Memilih Sprei", "body": body})
	if code != 201 || p["status"] != "draft" || p["slug"] != "7-panduan-memilih-sprei" || p["published_at"] != nil ||
		p["is_featured"] != false || p["excerpt"] != nil || p["cover_url"] != nil || p["category"] != nil {
		t.Fatalf("create: %d %v", code, p)
	}
	id := p["id"].(string)
	if p["excerpt_preview"] != "Seprai dan selimut merupakan sarana pelengkap istirahat Anda." {
		t.Errorf("excerpt_preview %q is not the body's first paragraph", p["excerpt_preview"])
	}
	if a, _ := p["author"].(map[string]any); a == nil || a["name"] == "" {
		t.Errorf("author %v: the creator, by default (BR-133)", p["author"])
	}
	if len(p["tags"].([]any)) != 0 || len(p["products"].([]any)) != 0 {
		t.Errorf("tags and products start empty: %v", p)
	}

	t.Run("create checks its body", func(t *testing.T) {
		code, e := do("POST", "/v1/blog/posts", map[string]any{"title": "7 panduan, memilih SPREI!"})
		assertProblem(t, code, e, 422, "validation_failed", "title")
		code, e = do("POST", "/v1/blog/posts", map[string]any{"title": "Other", "slug": "7-panduan-memilih-sprei"})
		assertProblem(t, code, e, 422, "validation_failed", "slug")
		code, e = do("POST", "/v1/blog/posts", map[string]any{"title": "X", "cover_r2_key": "k"})
		assertProblem(t, code, e, 422, "unknown_field", "cover_r2_key")
		code, e = do("POST", "/v1/blog/posts", map[string]any{"title": "X", "category_id": nil})
		assertProblem(t, code, e, 422, "validation_failed", "category_id")
		code, e = do("POST", "/v1/blog/posts", map[string]any{"title": "X", "author_id": uuid.NewString()})
		assertProblem(t, code, e, 422, "validation_failed", "author_id")
		code, e = do("POST", "/v1/blog/posts", map[string]any{"title": "X", "status": "live"})
		assertProblem(t, code, e, 422, "validation_failed", "status")
	})
	t.Run("excerpt: written wins, empty is none", func(t *testing.T) {
		code, e := do("PATCH", "/v1/blog/posts/"+id, map[string]any{"excerpt": "Pilih paket sprei yang tepat."})
		if code != 200 || e["excerpt"] != "Pilih paket sprei yang tepat." || e["excerpt_preview"] != e["excerpt"] {
			t.Fatalf("written: %d %v", code, e)
		}
		code, e = do("PATCH", "/v1/blog/posts/"+id, map[string]any{"excerpt": "  "})
		if code != 200 || e["excerpt"] != nil || !strings.HasPrefix(e["excerpt_preview"].(string), "Seprai") {
			t.Fatalf("emptied: %d %v", code, e)
		}
	})
	t.Run("category and tags", func(t *testing.T) {
		catID := apiCreate(t, admin, "/v1/blog/categories", map[string]string{"name": "Bedding tips"})
		code, e := do("PATCH", "/v1/blog/posts/"+id, map[string]any{"category_id": catID,
			"tags": []string{" Bedding Tips ", "Panduan", "Bedding Tips"}})
		if c, _ := e["category"].(map[string]any); code != 200 || c == nil || c["name"] != "Bedding tips" {
			t.Fatalf("category: %d %v", code, e)
		}
		if tags := e["tags"].([]any); len(tags) != 2 || tags[0] != "Bedding Tips" || tags[1] != "Panduan" {
			t.Errorf("tags are trimmed with repeats dropped: %v", tags)
		}
		code, e = do("PATCH", "/v1/blog/posts/"+id, map[string]any{"tags": []string{"ok", " "}})
		assertProblem(t, code, e, 422, "validation_failed", "tags")
		code, e = do("PATCH", "/v1/blog/posts/"+id, map[string]any{"tags": strings.Split("a,b,c,d,e,f,g,h,i,j,k", ",")})
		assertProblem(t, code, e, 422, "validation_failed", "tags")

		gone := apiCreate(t, admin, "/v1/blog/categories", map[string]string{"name": "Gone"})
		if code, _ := do("DELETE", "/v1/blog/categories/"+gone, nil); code != 204 {
			t.Fatalf("archive category: %d", code)
		}
		code, e = do("PATCH", "/v1/blog/posts/"+id, map[string]any{"category_id": gone})
		assertProblem(t, code, e, 422, "validation_failed", "category_id")
		code, e = do("PATCH", "/v1/blog/posts/"+id, map[string]any{"category_id": nil})
		if code != 200 || e["category"] != nil {
			t.Errorf("category null clears it: %d %v", code, e)
		}
	})
	t.Run("publishing sets the date; a published post keeps one", func(t *testing.T) {
		code, e := do("PATCH", "/v1/blog/posts/"+id, map[string]any{"status": "published"})
		if code != 200 || e["status"] != "published" || e["published_at"] == nil {
			t.Fatalf("publish: %d %v", code, e)
		}
		code, e = do("PATCH", "/v1/blog/posts/"+id, map[string]any{"published_at": nil})
		assertProblem(t, code, e, 422, "validation_failed", "published_at")
		code, e = do("PATCH", "/v1/blog/posts/"+id, map[string]any{"published_at": "2026-12-24T09:00:00"})
		assertProblem(t, code, e, 422, "validation_failed", "")
		code, e = do("PATCH", "/v1/blog/posts/"+id, map[string]any{"status": "draft"})
		if code != 200 || e["status"] != "draft" || e["published_at"] == nil {
			t.Errorf("back to draft keeps the date: %d %v", code, e)
		}
	})
	t.Run("one featured post per shop", func(t *testing.T) {
		other := apiCreate(t, admin, "/v1/blog/posts", map[string]any{"title": "Featured first", "is_featured": true})
		code, e := do("PATCH", "/v1/blog/posts/"+id, map[string]any{"is_featured": true})
		if code != 200 || e["is_featured"] != true {
			t.Fatalf("feature: %d %v", code, e)
		}
		if _, o := do("GET", "/v1/blog/posts/"+other, nil); o["is_featured"] != false {
			t.Errorf("the post that had the flag keeps it: %v", o)
		}
	})
	t.Run("linked products keep their order", func(t *testing.T) {
		p1 := apiCreate(t, admin, "/v1/products", map[string]string{"title": "Plain White"})
		p2 := apiCreate(t, admin, "/v1/products", map[string]string{"title": "Steel Light"})
		code, e := do("PATCH", "/v1/blog/posts/"+id, map[string]any{"product_ids": []string{p2, p1}})
		prods, _ := e["products"].([]any)
		if code != 200 || len(prods) != 2 || prods[0].(map[string]any)["id"] != p2 || prods[1].(map[string]any)["title"] != "Plain White" {
			t.Fatalf("products: %d %v", code, e)
		}
		code, e = do("PATCH", "/v1/blog/posts/"+id, map[string]any{"product_ids": []string{p1, p1}})
		assertProblem(t, code, e, 422, "validation_failed", "product_ids")
		code, e = do("PATCH", "/v1/blog/posts/"+id, map[string]any{"product_ids": []string{p1, uuid.NewString()}})
		assertProblem(t, code, e, 422, "validation_failed", "product_ids")
	})
	t.Run("the cover is presigned under the post and must exist", func(t *testing.T) {
		code, e := do("POST", "/v1/media/presign", map[string]any{"purpose": "blog_cover", "post_id": id,
			"mime_type": "image/png", "bytes": 4, "sha256": strings.Repeat("ab", 32)})
		key, _ := e["r2_key"].(string)
		if code != 200 || !strings.Contains(key, "/blogs/"+id+"/") || !strings.HasSuffix(key, ".png") {
			t.Fatalf("presign: %d %v", code, e)
		}
		code, e = do("PATCH", "/v1/blog/posts/"+id, map[string]any{"cover_r2_key": key})
		assertProblem(t, code, e, 422, "validation_failed", "cover_r2_key")
		if err := testFiles(t).Put(ctx, key, strings.NewReader("\x89PNG"), 4, "image/png"); err != nil {
			t.Fatalf("upload: %v", err)
		}
		code, e = do("PATCH", "/v1/blog/posts/"+id, map[string]any{"cover_r2_key": key})
		if u, _ := e["cover_url"].(string); code != 200 || e["cover_r2_key"] != key || !strings.HasSuffix(u, "/"+key) {
			t.Fatalf("cover: %d %v", code, e)
		}
		otherPost := apiCreate(t, admin, "/v1/blog/posts", map[string]string{"title": "Another"})
		code, e = do("PATCH", "/v1/blog/posts/"+otherPost, map[string]any{"cover_r2_key": key})
		assertProblem(t, code, e, 422, "validation_failed", "cover_r2_key")
		code, e = do("POST", "/v1/media/presign", map[string]any{"purpose": "product_image", "post_id": id,
			"mime_type": "image/png", "bytes": 4, "sha256": strings.Repeat("ab", 32)})
		assertProblem(t, code, e, 422, "validation_failed", "post_id")
	})
	t.Run("a body image is presigned under the post, with its public URL", func(t *testing.T) {
		code, e := do("POST", "/v1/media/presign", map[string]any{"purpose": "blog_image", "post_id": id,
			"mime_type": "image/webp", "bytes": 4, "sha256": strings.Repeat("cd", 32)})
		key, _ := e["r2_key"].(string)
		u, _ := e["public_url"].(string)
		if code != 200 || !strings.Contains(key, "/blogs/"+id+"/") || !strings.HasSuffix(u, "/"+key) {
			t.Fatalf("presign: %d %v", code, e)
		}
		code, e = do("POST", "/v1/media/presign", map[string]any{"purpose": "blog_image",
			"mime_type": "image/webp", "bytes": 4, "sha256": strings.Repeat("cd", 32)})
		assertProblem(t, code, e, 422, "validation_failed", "post_id")
		ops := signInAnotherUser(ctx, t, store, tenantID, auth.RoleOps)
		code, e = apiClient(t, ops)("POST", "/v1/media/presign", map[string]any{"purpose": "blog_image", "post_id": id,
			"mime_type": "image/webp", "bytes": 4, "sha256": strings.Repeat("cd", 32)})
		assertProblem(t, code, e, 403, "permission_denied", "")
	})
	t.Run("list filters and pages; delete archives and clears featured", func(t *testing.T) {
		code, l := do("GET", "/v1/blog/posts?q=panduan", nil)
		if code != 200 || len(l["data"].([]any)) != 1 {
			t.Fatalf("q: %d %v", code, l)
		}
		_, l = do("GET", "/v1/blog/posts?status=draft", nil)
		for _, d := range l["data"].([]any) {
			if d.(map[string]any)["status"] != "draft" {
				t.Errorf("status filter let through %v", d)
			}
		}
		seen := 0
		path := "/v1/blog/posts?limit=1"
		for pages := 0; path != ""; pages++ {
			code, l = do("GET", path, nil)
			if code != 200 || pages > 10 {
				t.Fatalf("page: %d %v", code, l)
			}
			seen += len(l["data"].([]any))
			path = ""
			if c, ok := l["next_cursor"].(string); ok {
				path = "/v1/blog/posts?limit=1&cursor=" + c
			}
		}
		if seen != 3 {
			t.Errorf("paged through %d posts, want 3", seen)
		}

		if code, _ := do("DELETE", "/v1/blog/posts/"+id, nil); code != 204 {
			t.Fatalf("delete: %d", code)
		}
		code, e := do("GET", "/v1/blog/posts/"+id, nil)
		if code != 200 || e["archived_at"] == nil || e["is_featured"] != false {
			t.Errorf("archived: %d %v", code, e)
		}
		code, e = do("PATCH", "/v1/blog/posts/"+id, map[string]any{"title": "Revived"})
		assertProblem(t, code, e, 422, "validation_failed", "status")
		_, live := do("GET", "/v1/blog/posts", nil)
		_, archived := do("GET", "/v1/blog/posts?archived=true", nil)
		if len(live["data"].([]any)) != 2 || len(archived["data"].([]any)) != 1 {
			t.Errorf("after delete: live %d, archived %d", len(live["data"].([]any)), len(archived["data"].([]any)))
		}
	})
	t.Run("ops reads but cannot write", func(t *testing.T) {
		ops := signInAnotherUser(ctx, t, store, tenantID, auth.RoleOps)
		opsDo := apiClient(t, ops)
		if code, _ := opsDo("GET", "/v1/blog/posts/"+id, nil); code != 200 {
			t.Errorf("ops read: %d", code)
		}
		code, e := opsDo("PATCH", "/v1/blog/posts/"+id, map[string]any{"title": "Nope"})
		assertProblem(t, code, e, 403, "permission_denied", "")
	})
}
