package storefront

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

// variantBySKU is a seeded variant's id.
func variantBySKU(t *testing.T, store *db.Store, tenantID uuid.UUID, sku string) string {
	t.Helper()
	ctx := t.Context()
	var id string
	err := store.InTenantTx(tenant.NewContext(ctx, tenantID), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT id::text FROM variants WHERE sku = $1`, sku).Scan(&id)
	})
	if err != nil {
		t.Fatalf("variant %s: %v", sku, err)
	}
	return id
}

func assertProblem(t *testing.T, what string, code int, body map[string]any, status int, errCode, field string) {
	t.Helper()
	typ, _ := body["type"].(string)
	if code != status || !strings.HasSuffix(typ, "/"+errCode) {
		t.Errorf("%s: %d %v, want %d %s", what, code, body, status, errCode)
		return
	}
	if field == "" {
		return
	}
	errs, _ := body["errors"].([]any)
	if len(errs) == 0 || errs[0].(map[string]any)["field"] != field {
		t.Errorf("%s: errors %v, want field %s", what, body["errors"], field)
	}
}

// TestCarts is P1-204's acceptance (04-api-spec.md §11.5; BR-005, BR-046,
// BR-087, BR-089, BR-090).
func TestCarts(t *testing.T) {
	store := openStore(t)
	tenantA, keyA := seedKey(t, store, "https://a.example")
	tenantB, keyB := seedKey(t, store, "https://b.example")
	seedCatalog(t, store, tenantA)
	seedCatalog(t, store, tenantB)
	a, b := newShop(t, store, keyA), newShop(t, store, keyB)
	sale := variantBySKU(t, store, tenantA, "TS-BLK-S")
	full := variantBySKU(t, store, tenantA, "TS-BLK-M")
	archived := variantBySKU(t, store, tenantA, "TS-GONE")
	theirs := variantBySKU(t, store, tenantB, "TS-BLK-S")

	code, cart, raw := a.do(t, "POST", "/v1/storefront/carts", nil)
	if code != 201 || cart["item_count"] != float64(0) || cart["subtotal"] != float64(0) || len(cart["items"].([]any)) != 0 {
		t.Fatalf("create: %d %s", code, raw)
	}
	id := cart["cart_id"].(string)
	if u := uuid.MustParse(id); u.Version() != 4 {
		t.Errorf("cart id is v%d; a cart id is a credential and must be a random v4 (BR-005)", u.Version())
	}
	items := "/v1/storefront/carts/" + id + "/items/"

	code, body, _ := a.do(t, "POST", "/v1/storefront/carts", map[string]any{"price": 1})
	assertProblem(t, "create with a price", code, body, 422, "unknown_field", "price")

	t.Run("setting a quantity prices it now", func(t *testing.T) {
		code, cart, raw := a.do(t, "PUT", items+sale, map[string]any{"qty": 2})
		if code != 200 || cart["item_count"] != float64(2) || cart["subtotal"] != float64(29800000) {
			t.Fatalf("add: %d %s", code, raw)
		}
		it := cart["items"].([]any)[0].(map[string]any)
		if it["title"] != "Basic Tee — Black / S" || it["unit_price"] != float64(14900000) || it["line_total"] != float64(29800000) ||
			it["product_slug"] != "basic-tee" || it["available"] != true || !strings.HasSuffix(it["image_url"].(string), "tee-200.webp") {
			t.Errorf("item %v", it)
		}
		// Setting again sets, it does not add (BR-087).
		if _, cart, _ = a.do(t, "PUT", items+sale, map[string]any{"qty": 3}); cart["item_count"] != float64(3) {
			t.Errorf("set again: %v", cart)
		}
		_, cart, _ = a.do(t, "PUT", items+full, map[string]any{"qty": 1})
		if cart["item_count"] != float64(4) || cart["subtotal"] != float64(3*14900000+21900000) {
			t.Errorf("two items: %v", cart)
		}
	})

	t.Run("what may not go in", func(t *testing.T) {
		for what, req := range map[string]struct {
			variant string
			body    any
			code    string
			field   string
		}{
			"qty over 999":            {sale, map[string]any{"qty": 1000}, "validation_failed", "qty"},
			"negative qty":            {sale, map[string]any{"qty": -1}, "validation_failed", "qty"},
			"no qty":                  {sale, map[string]any{}, "validation_failed", "qty"},
			"a price (BR-089)":        {sale, map[string]any{"qty": 1, "price": 100}, "unknown_field", "price"},
			"an archived variant":     {archived, map[string]any{"qty": 1}, "validation_failed", "variant_id"},
			"another shop's variant":  {theirs, map[string]any{"qty": 1}, "validation_failed", "variant_id"},
			"a variant that is no id": {"nope", map[string]any{"qty": 1}, "validation_failed", "variant_id"},
		} {
			code, body, _ := a.do(t, "PUT", items+req.variant, req.body)
			assertProblem(t, what, code, body, 422, req.code, req.field)
		}
	})

	t.Run("an item that goes out of view stays, unpriced", func(t *testing.T) {
		execTenant(t, store, tenantA, `UPDATE variants SET archived_at = now() WHERE sku = 'TS-BLK-M'`)
		code, cart, raw := a.get(t, "/v1/storefront/carts/"+id)
		if code != 200 || cart["subtotal"] != float64(3*14900000) || cart["item_count"] != float64(4) {
			t.Fatalf("after archive: %d %s", code, raw)
		}
		gone := cart["items"].([]any)[1].(map[string]any)
		if gone["available"] != false || gone["unit_price"] != nil || gone["line_total"] != nil || gone["sku"] != "TS-BLK-M" {
			t.Errorf("unavailable item %v", gone)
		}
		// 0 clears it even so (BR-090).
		if _, cart, _ := a.do(t, "PUT", items+full, map[string]any{"qty": 0}); len(cart["items"].([]any)) != 1 {
			t.Errorf("remove unavailable: %v", cart)
		}
	})

	t.Run("404s", func(t *testing.T) {
		for what, path := range map[string]string{
			"unknown":   "/v1/storefront/carts/" + uuid.NewString(),
			"not an id": "/v1/storefront/carts/abc",
		} {
			code, body, _ := a.get(t, path)
			assertProblem(t, what, code, body, 404, "not_found", "")
		}
		// Holding the id is not enough with another shop's key (BR-003).
		code, body, _ := b.get(t, "/v1/storefront/carts/"+id)
		assertProblem(t, "another shop's key", code, body, 404, "not_found", "")

		_, expired, _ := a.do(t, "POST", "/v1/storefront/carts", nil)
		_, done, _ := a.do(t, "POST", "/v1/storefront/carts", nil)
		ctx := context.WithoutCancel(t.Context())
		_ = store.InTenantTx(tenant.NewContext(ctx, tenantA), func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE carts SET expires_at = now() - interval '1 second' WHERE id = $1;`, expired["cart_id"])
			if err == nil {
				_, err = tx.Exec(ctx, `UPDATE carts SET status = 'checked_out' WHERE id = $1`, done["cart_id"])
			}
			return err
		})
		for what, c := range map[string]map[string]any{"expired": expired, "checked out": done} {
			path := "/v1/storefront/carts/" + c["cart_id"].(string)
			code, body, _ := a.get(t, path)
			assertProblem(t, what+" GET", code, body, 404, "not_found", "")
			code, body, _ = a.do(t, "PUT", path+"/items/"+sale, map[string]any{"qty": 1})
			assertProblem(t, what+" PUT", code, body, 404, "not_found", "")
		}
	})
}
