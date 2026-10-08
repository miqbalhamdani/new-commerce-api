package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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

// Brand routes (P1-021) in the isolation and audit suites. Tenant A acts on
// tenant B's brand by id; B's name is the marker that must never come back.
func init() {
	byID := func(method string) func(t *testing.T, s seeded) *http.Request {
		return func(t *testing.T, s seeded) *http.Request {
			return bodyRequest(t, method, "/v1/brands/"+s.otherID, s.accessToken, map[string]string{"name": "Renamed"})
		}
	}
	isolationCases = append(isolationCases,
		isolationCase{route: route{"GET", "/v1/brands"}, seed: seedBrand,
			request: func(t *testing.T, s seeded) *http.Request {
				return bearerRequest(t, http.MethodGet, "/v1/brands?limit=200", s.accessToken)
			}},
		isolationCase{route: route{"POST", "/v1/brands"}, seed: seedBrand,
			request: func(t *testing.T, s seeded) *http.Request {
				return bodyRequest(t, http.MethodPost, "/v1/brands", s.accessToken, map[string]string{"name": "New " + uuid.NewString()})
			}},
		isolationCase{route: route{"GET", "/v1/brands/{id}"}, seed: seedBrand, request: byID(http.MethodGet)},
		isolationCase{route: route{"PATCH", "/v1/brands/{id}"}, seed: seedBrand, request: byID(http.MethodPatch)},
		isolationCase{route: route{"DELETE", "/v1/brands/{id}"}, seed: seedBrand, request: byID(http.MethodDelete)},
	)

	auditCases = append(auditCases,
		auditCase{route: route{"POST", "/v1/brands"}, request: func(t *testing.T, s seeded) (*http.Request, string, string) {
			return bodyRequest(t, http.MethodPost, "/v1/brands", s.accessToken, map[string]string{"name": "Audited " + uuid.NewString()}), "brand", ""
		}},
		auditCase{route: route{"PATCH", "/v1/brands/{id}"}, request: func(t *testing.T, s seeded) (*http.Request, string, string) {
			id := createBrand(t, s, "Before "+uuid.NewString())
			return bodyRequest(t, http.MethodPatch, "/v1/brands/"+id, s.accessToken, map[string]string{"name": "After " + uuid.NewString()}), "brand", id
		}},
		auditCase{route: route{"DELETE", "/v1/brands/{id}"}, request: func(t *testing.T, s seeded) (*http.Request, string, string) {
			id := createBrand(t, s, "Doomed "+uuid.NewString())
			return bearerRequest(t, http.MethodDelete, "/v1/brands/"+id, s.accessToken), "brand", id
		}},
	)
}

// seedBrand is a signed-in admin plus one brand whose name is the marker.
func seedBrand(ctx context.Context, t *testing.T, store *db.Store, tenantID uuid.UUID) seeded {
	t.Helper()
	s := seedSignedInUserWithRole(ctx, t, store, tenantID, auth.RoleAdmin)
	id := uuid.Must(uuid.NewV7())
	s.marker = "Brand " + id.String()
	s.id = id.String()
	if err := store.InTenantTx(tenant.NewContext(ctx, tenantID), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO brands (id, tenant_id, name, slug) VALUES ($1, $2, $3, slugify($3))`,
			id, tenantID, s.marker)
		return err
	}); err != nil {
		t.Fatalf("seed brand: %v", err)
	}
	return s
}

// TestBrands is P1-021's acceptance over the real server (04-api-spec.md §6.1).
func TestBrands(t *testing.T) {
	ctx := t.Context()
	store := openAppStore(ctx, t)
	srv := newServer(t)
	admin := seedSignedInUserWithRole(ctx, t, store, uuid.Must(uuid.NewV7()), auth.RoleAdmin)

	do := func(method, path string, body any) (int, map[string]any) {
		t.Helper()
		var r *http.Request
		if body == nil {
			r = bearerRequest(t, method, path, admin.accessToken)
		} else {
			r = bodyRequest(t, method, path, admin.accessToken, body)
		}
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, r)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	code, b := do("POST", "/v1/brands", map[string]any{"name": "Erigo Café"})
	if code != http.StatusCreated || b["slug"] != "erigo-cafe" || b["archived_at"] != nil {
		t.Fatalf("create: %d %v", code, b)
	}
	id := b["id"].(string)

	t.Run("slug is server-managed", func(t *testing.T) {
		code, p := do("POST", "/v1/brands", map[string]any{"name": "X", "slug": "x"})
		assertProblem(t, code, p, 422, "validation_failed", "slug")
	})
	t.Run("unknown field", func(t *testing.T) {
		code, p := do("POST", "/v1/brands", map[string]any{"name": "X", "colour": "red"})
		assertProblem(t, code, p, 422, "unknown_field", "colour")
	})
	t.Run("null name", func(t *testing.T) {
		code, p := do("POST", "/v1/brands", map[string]any{"name": nil})
		assertProblem(t, code, p, 422, "validation_failed", "name")
	})
	t.Run("a name that slugifies the same, archived included, is 422 on name", func(t *testing.T) {
		code, p := do("POST", "/v1/brands", map[string]any{"name": "Old Label"})
		if code != 201 {
			t.Fatalf("create: %d %v", code, p)
		}
		if code, _ := do("DELETE", "/v1/brands/"+p["id"].(string), nil); code != 204 {
			t.Fatalf("archive: %d", code)
		}
		code, p = do("POST", "/v1/brands", map[string]any{"name": "old label!"})
		assertProblem(t, code, p, 422, "validation_failed", "name")
	})
	t.Run("rename re-derives the slug", func(t *testing.T) {
		code, p := do("PATCH", "/v1/brands/"+id, map[string]any{"name": "Erigo Apparel"})
		if code != 200 || p["slug"] != "erigo-apparel" {
			t.Fatalf("rename: %d %v", code, p)
		}
	})
	t.Run("list pages by name with a cursor; archived only on request", func(t *testing.T) {
		for _, n := range []string{"Zeta", "Alpha", "Mid"} {
			if code, p := do("POST", "/v1/brands", map[string]any{"name": n}); code != 201 {
				t.Fatalf("create %s: %d %v", n, code, p)
			}
		}
		var names []string
		path := "/v1/brands?limit=2"
		for pages := 0; path != ""; pages++ {
			code, p := do("GET", path, nil)
			if code != 200 || pages > 5 {
				t.Fatalf("list: %d %v", code, p)
			}
			for _, d := range p["data"].([]any) {
				names = append(names, d.(map[string]any)["name"].(string))
			}
			path = ""
			if c, ok := p["next_cursor"].(string); ok {
				path = "/v1/brands?limit=2&cursor=" + c
			}
		}
		if want := "Alpha,Erigo Apparel,Mid,Zeta"; strings.Join(names, ",") != want {
			t.Errorf("names %v, want %s", names, want)
		}
		code, p := do("GET", "/v1/brands?archived=true", nil)
		if code != 200 || len(p["data"].([]any)) != 1 {
			t.Errorf("archived list: %d %v", code, p)
		}
		code, p = do("GET", "/v1/brands?q=lph", nil)
		if code != 200 || len(p["data"].([]any)) != 1 {
			t.Errorf("q: %d %v", code, p)
		}
	})
	t.Run("a bad cursor or id", func(t *testing.T) {
		code, p := do("GET", "/v1/brands?cursor=nope", nil)
		assertProblem(t, code, p, 422, "validation_failed", "cursor")
		code, p = do("GET", "/v1/brands/not-a-uuid", nil)
		assertProblem(t, code, p, 404, "not_found", "")
	})
	t.Run("ops reads but cannot write", func(t *testing.T) {
		ops := seedSignedInUserWithRole(ctx, t, store, uuid.Must(uuid.NewV7()), auth.RoleOps)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, bearerRequest(t, http.MethodGet, "/v1/brands", ops.accessToken))
		if rec.Code != 200 {
			t.Errorf("ops list: %d", rec.Code)
		}
		rec = httptest.NewRecorder()
		srv.ServeHTTP(rec, bodyRequest(t, http.MethodPost, "/v1/brands", ops.accessToken, map[string]string{"name": "Nope"}))
		if rec.Code != 403 || !strings.Contains(rec.Body.String(), "brands:write") {
			t.Errorf("ops create: %d %s", rec.Code, rec.Body)
		}
	})
}

// bodyRequest is a bearer request with a JSON body.
func bodyRequest(t *testing.T, method, path, token string, body any) *http.Request {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encode body: %v", err)
	}
	r := httptest.NewRequest(method, path, strings.NewReader(string(b)))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	return r
}

// createBrand makes a brand through the API and returns its id.
func createBrand(t *testing.T, s seeded, name string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	newServer(t).ServeHTTP(rec, bodyRequest(t, http.MethodPost, "/v1/brands", s.accessToken, map[string]string{"name": name}))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create brand: %d %s", rec.Code, rec.Body)
	}
	var b struct {
		ID string `json:"id"`
	}
	_ = json.NewDecoder(io.Reader(rec.Body)).Decode(&b)
	return b.ID
}

// assertProblem checks a problem response's status, code and (when given) the
// field its errors name.
func assertProblem(t *testing.T, code int, p map[string]any, status int, errCode, field string) {
	t.Helper()
	if code != status {
		t.Errorf("status %d, want %d: %v", code, status, p)
		return
	}
	if typ, _ := p["type"].(string); !strings.HasSuffix(typ, "/"+errCode) {
		t.Errorf("type %v, want .../%s", p["type"], errCode)
	}
	if field == "" {
		return
	}
	errs, _ := p["errors"].([]any)
	for _, e := range errs {
		if e.(map[string]any)["field"] == field {
			return
		}
	}
	t.Errorf("errors %v do not name %s", fmt.Sprint(errs), field)
}
