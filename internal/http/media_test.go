package httpapi_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/storage"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

func init() {
	isolationCases = append(isolationCases,
		isolationCase{route: route{"POST", "/v1/media/presign"}, seed: seedMedia,
			request: func(t *testing.T, s seeded) *http.Request {
				return bodyRequest(t, http.MethodPost, "/v1/media/presign", s.accessToken, map[string]any{
					"purpose": "product_image", "product_id": s.otherProduct, "mime_type": "image/png",
					"bytes": 10, "sha256": strings.Repeat("a", 64)})
			}},
		isolationCase{route: route{"POST", "/v1/media/confirm"}, seed: seedMedia,
			request: func(t *testing.T, s seeded) *http.Request {
				return bodyRequest(t, http.MethodPost, "/v1/media/confirm", s.accessToken, map[string]any{
					"r2_key": s.otherKey, "product_id": s.otherProduct})
			}},
		isolationCase{route: route{"PATCH", "/v1/media/{id}"}, seed: seedMedia,
			request: func(t *testing.T, s seeded) *http.Request {
				return bodyRequest(t, http.MethodPatch, "/v1/media/"+s.otherID, s.accessToken, map[string]any{"variant_id": nil})
			}},
		isolationCase{route: route{"DELETE", "/v1/media/{id}"}, seed: seedMedia,
			request: func(t *testing.T, s seeded) *http.Request {
				return bearerRequest(t, http.MethodDelete, "/v1/media/"+s.otherID, s.accessToken)
			}},
		isolationCase{route: route{"PATCH", "/v1/products/{id}/media/order"}, seed: seedMedia,
			request: func(t *testing.T, s seeded) *http.Request {
				return bodyRequest(t, http.MethodPatch, "/v1/products/"+s.otherProduct+"/media/order", s.accessToken,
					map[string]any{"media_ids": []string{s.otherID}})
			}},
	)
	auditExempt[route{"POST", "/v1/media/presign"}] = "signs an upload URL; writes nothing until confirm"
	auditCases = append(auditCases,
		auditCase{route: route{"POST", "/v1/media/confirm"}, request: func(t *testing.T, s seeded) (*http.Request, string, string) {
			product := apiCreate(t, s, "/v1/products", map[string]any{"title": "Tee"})
			key := upload(t, s, product, pngBytes(t, 4, 4))
			return bodyRequest(t, http.MethodPost, "/v1/media/confirm", s.accessToken,
				map[string]any{"r2_key": key, "product_id": product}), "media", ""
		}},
		auditCase{route: route{"PATCH", "/v1/media/{id}"}, request: func(t *testing.T, s seeded) (*http.Request, string, string) {
			_, id := confirmed(t, s)
			return bodyRequest(t, http.MethodPatch, "/v1/media/"+id, s.accessToken, map[string]any{"variant_id": nil}), "media", id
		}},
		auditCase{route: route{"DELETE", "/v1/media/{id}"}, request: func(t *testing.T, s seeded) (*http.Request, string, string) {
			_, id := confirmed(t, s)
			return bearerRequest(t, http.MethodDelete, "/v1/media/"+id, s.accessToken), "media", id
		}},
		auditCase{route: route{"PATCH", "/v1/products/{id}/media/order"}, request: func(t *testing.T, s seeded) (*http.Request, string, string) {
			product, id := confirmed(t, s)
			return bodyRequest(t, http.MethodPatch, "/v1/products/"+product+"/media/order", s.accessToken,
				map[string]any{"media_ids": []string{id}}), "product", product
		}},
	)
}

// seedMedia is a signed-in admin plus a product with one image row whose key
// is the marker.
func seedMedia(ctx context.Context, t *testing.T, store *db.Store, tenantID uuid.UUID) seeded {
	t.Helper()
	s := seedSignedInUserWithRole(ctx, t, store, tenantID, auth.RoleAdmin)
	product, media := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	s.product, s.id = product.String(), media.String()
	s.key = tenantID.String() + "/products/" + product.String() + "/" + strings.Repeat("b", 64) + ".png"
	s.marker = s.key
	if err := store.InTenantTx(tenant.NewContext(ctx, tenantID), func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO products (id, tenant_id, title, slug) VALUES ($1, $2, 'Tee', $3)`,
			product, tenantID, "tee-"+product.String()); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO product_media (id, tenant_id, product_id, r2_key, mime_type, bytes)
			VALUES ($1, $2, $3, $4, 'image/png', 1)`, media, tenantID, product, s.key)
		return err
	}); err != nil {
		t.Fatalf("seed media: %v", err)
	}
	return s
}

// TestMedia is P1-043's acceptance: presign, a direct upload, confirm with a
// HEAD check, attach, reorder and delete (BR-051, BR-053).
func TestMedia(t *testing.T) {
	ctx := t.Context()
	store := openAppStore(ctx, t)
	tenantID := uuid.Must(uuid.NewV7())
	admin := seedSignedInUserWithRole(ctx, t, store, tenantID, auth.RoleAdmin)
	do := apiClient(t, admin)
	product := apiCreate(t, admin, "/v1/products", map[string]any{"title": "Erigo Basic Tee"})
	img := pngBytes(t, 8, 6)

	key := upload(t, admin, product, img)
	if !strings.HasPrefix(key, tenantID.String()+"/products/"+product+"/") || !strings.Contains(key, sha(img)) {
		t.Fatalf("key %s does not carry the tenant, product and content hash", key)
	}
	code, m := do("POST", "/v1/media/confirm", map[string]any{"r2_key": key, "product_id": product})
	if code != 201 || m["mime_type"] != "image/png" || m["bytes"] != float64(len(img)) || m["position"] != float64(0) ||
		!strings.HasSuffix(m["url"].(string), key) {
		t.Fatalf("confirm: %d %v", code, m)
	}
	first := m["id"].(string)

	t.Run("confirming twice is the same image; a derivatives job is queued once", func(t *testing.T) {
		code, again := do("POST", "/v1/media/confirm", map[string]any{"r2_key": key, "product_id": product})
		if code != 201 || again["id"] != first {
			t.Errorf("second confirm: %d %v", code, again)
		}
		var n int
		_ = store.InTenantTx(tenant.NewContext(ctx, tenantID), func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE kind = 'image_derivatives' AND params->>'media_id' = $1`, first).Scan(&n)
		})
		if n != 1 {
			t.Errorf("%d derivative jobs, want 1", n)
		}
	})
	t.Run("a key never uploaded, or not an image, or another product's, is 422", func(t *testing.T) {
		missing := tenantID.String() + "/products/" + product + "/" + strings.Repeat("c", 64) + ".png"
		code, p := do("POST", "/v1/media/confirm", map[string]any{"r2_key": missing, "product_id": product})
		assertProblem(t, code, p, 422, "validation_failed", "r2_key")

		code, p = do("POST", "/v1/media/confirm", map[string]any{"r2_key": key, "product_id": uuid.NewString()})
		assertProblem(t, code, p, 422, "validation_failed", "r2_key")

		textKey := tenantID.String() + "/products/" + product + "/" + strings.Repeat("d", 64) + ".png"
		if err := testFiles(t).Put(ctx, textKey, strings.NewReader("hello"), 5, "text/plain"); err != nil {
			t.Fatalf("put: %v", err)
		}
		code, p = do("POST", "/v1/media/confirm", map[string]any{"r2_key": textKey, "product_id": product})
		assertProblem(t, code, p, 422, "validation_failed", "r2_key")
	})
	t.Run("presign checks type, size and permission", func(t *testing.T) {
		body := func(purpose, mime string, n int) map[string]any {
			b := map[string]any{"purpose": purpose, "mime_type": mime, "bytes": n, "sha256": sha(img)}
			if purpose == "product_image" {
				b["product_id"] = product
			}
			return b
		}
		code, p := do("POST", "/v1/media/presign", body("product_image", "image/gif", 10))
		assertProblem(t, code, p, 422, "validation_failed", "mime_type")
		code, p = do("POST", "/v1/media/presign", body("product_image", "image/png", 21<<20))
		assertProblem(t, code, p, 422, "validation_failed", "bytes")
		code, p = do("POST", "/v1/media/presign", body("product_import", "text/csv", 1000))
		if code != 200 || !strings.Contains(p["r2_key"].(string), "/jobs/") || !strings.HasSuffix(p["r2_key"].(string), "/upload.csv") {
			t.Errorf("import presign: %d %v", code, p)
		}
		ops := signInAnotherUser(ctx, t, store, tenantID, auth.RoleOps)
		code, p = apiClient(t, ops)("POST", "/v1/media/presign", body("product_import", "text/csv", 1000))
		assertProblem(t, code, p, 403, "permission_denied", "")
	})
	t.Run("attach to a variant of this product only", func(t *testing.T) {
		other := apiCreate(t, admin, "/v1/products", map[string]any{"title": "Other"})
		foreign := apiCreate(t, admin, "/v1/products/"+other+"/variants", map[string]any{"option_values": []string{}})
		code, p := do("PATCH", "/v1/media/"+first, map[string]any{"variant_id": foreign})
		assertProblem(t, code, p, 422, "validation_failed", "variant_id")
		own := apiCreate(t, admin, "/v1/products/"+product+"/variants", map[string]any{"option_values": []string{}})
		code, p = do("PATCH", "/v1/media/"+first, map[string]any{"variant_id": own})
		if code != 200 || p["variant_id"] != own {
			t.Fatalf("attach: %d %v", code, p)
		}
		code, p = do("PATCH", "/v1/media/"+first, map[string]any{"variant_id": nil})
		if code != 200 || p["variant_id"] != nil {
			t.Errorf("detach: %d %v", code, p)
		}
	})
	t.Run("reorder takes every image exactly once", func(t *testing.T) {
		second := mustConfirm(t, admin, product, pngBytes(t, 2, 2))
		code, p := do("PATCH", "/v1/products/"+product+"/media/order", map[string]any{"media_ids": []string{second}})
		assertProblem(t, code, p, 422, "validation_failed", "media_ids")
		code, p = do("PATCH", "/v1/products/"+product+"/media/order", map[string]any{"media_ids": []string{second, first}})
		if code != 200 || p["data"].([]any)[0].(map[string]any)["id"] != second {
			t.Fatalf("reorder: %d %v", code, p)
		}
		_, prod := do("GET", "/v1/products/"+product, nil)
		if media := prod["media"].([]any); len(media) != 2 || media[0].(map[string]any)["id"] != second {
			t.Errorf("product media %v", prod["media"])
		}
	})
	t.Run("delete removes the row and the object", func(t *testing.T) {
		if code, p := do("DELETE", "/v1/media/"+first, nil); code != 204 {
			t.Fatalf("delete: %d %v", code, p)
		}
		if _, _, err := testFiles(t).Head(ctx, key); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("object after delete: %v", err)
		}
	})
}

// upload presigns and PUTs an image straight to the store, as the browser
// does, and returns its key.
func upload(t *testing.T, s seeded, product string, img []byte) string {
	t.Helper()
	code, p := apiClient(t, s)("POST", "/v1/media/presign", map[string]any{"purpose": "product_image",
		"product_id": product, "mime_type": "image/png", "bytes": len(img), "sha256": sha(img)})
	if code != 200 {
		t.Fatalf("presign: %d %v", code, p)
	}
	req, _ := http.NewRequest(http.MethodPut, p["upload_url"].(string), bytes.NewReader(img))
	req.Header.Set("Content-Type", "image/png")
	res, err := http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("PUT to the store: %v %v (is MinIO running? make storage-init)", err, res)
	}
	_ = res.Body.Close()
	return p["r2_key"].(string)
}

func mustConfirm(t *testing.T, s seeded, product string, img []byte) string {
	t.Helper()
	code, m := apiClient(t, s)("POST", "/v1/media/confirm", map[string]any{"r2_key": upload(t, s, product, img), "product_id": product})
	if code != 201 {
		t.Fatalf("confirm: %d %v", code, m)
	}
	return m["id"].(string)
}

// confirmed is a fresh product with one confirmed image.
func confirmed(t *testing.T, s seeded) (product, media string) {
	t.Helper()
	product = apiCreate(t, s, "/v1/products", map[string]any{"title": "Tee"})
	return product, mustConfirm(t, s, product, pngBytes(t, 3, 3))
}

// pngBytes is a w×h PNG whose pixels differ by call, so each has its own hash.
func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	seed := uuid.New()
	for i := range w * h {
		img.Set(i%w, i/w, color.RGBA{seed[i%16], seed[(i+1)%16], seed[(i+2)%16], 255})
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func sha(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
