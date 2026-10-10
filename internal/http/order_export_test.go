package httpapi_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

func init() {
	isolationCases = append(isolationCases,
		// An export is scoped by RLS to the caller's tenant; B's marker must
		// not appear in the 202 answer (the file itself is read in
		// TestOrderExport).
		isolationCase{route: route{"POST", "/v1/orders/export"}, seed: seedOrderAt("pending"),
			request: func(t *testing.T, s seeded) *http.Request {
				return bodyRequest(t, http.MethodPost, "/v1/orders/export", s.accessToken, map[string]any{})
			}},
	)
	auditCases = append(auditCases,
		auditCase{route: route{"POST", "/v1/orders/export"},
			request: func(t *testing.T, s seeded) (*http.Request, string, string) {
				return bodyRequest(t, http.MethodPost, "/v1/orders/export", s.accessToken,
					map[string]any{"status": []string{"completed"}}), "job", ""
			}},
	)
}

// TestOrderExport is P1-107's acceptance (04-api-spec.md §5.6; BR-063,
// BR-064, BR-065): one CSV row per order line with order fields repeated, a
// BOM, integer amounts, filters honoured, and a link that regenerates.
func TestOrderExport(t *testing.T) {
	ctx := t.Context()
	store := openAppStore(ctx, t)
	tenantID := uuid.Must(uuid.NewV7())
	admin := seedSignedInUserWithRole(ctx, t, store, tenantID, auth.RoleAdmin)
	do := apiClient(t, admin)

	variant, sku := insertVariant(ctx, t, store, tenantID, "EXP")
	completed, _ := insertOrder(ctx, t, store, tenantID, "completed", "Rina")
	pending, _ := insertOrder(ctx, t, store, tenantID, "pending", "Dewi")
	if err := store.InTenantTx(tenant.NewContext(ctx, tenantID), func(tx pgx.Tx) error {
		for order, qtys := range map[string][]int{completed: {2, 1}, pending: {5}} {
			for _, qty := range qtys {
				if _, err := tx.Exec(ctx, `INSERT INTO order_lines (id, tenant_id, order_id, variant_id, sku_snapshot, title_snapshot, qty, unit_price)
					VALUES ($1, $2, $3, $4, $5, 'Entry Tee — Black / M', $6, 10000000)`,
					uuid.Must(uuid.NewV7()), tenantID, order, variant, sku, qty); err != nil {
					return err
				}
			}
		}
		_, err := tx.Exec(ctx, `UPDATE orders SET subtotal_amount = 30000000, total_amount = 30000000 WHERE id = $1`, completed)
		return err
	}); err != nil {
		t.Fatalf("seed lines: %v", err)
	}

	code, accepted := do("POST", "/v1/orders/export", map[string]any{"status": []string{"completed"}})
	if code != 202 {
		t.Fatalf("export: %d %v", code, accepted)
	}
	job := waitForJob(t, admin, accepted["job_id"].(string))
	if job["state"] != "done" {
		t.Fatalf("job %v", job)
	}
	result := job["result"].(map[string]any)
	url, _ := result["download_url"].(string)
	if url == "" || result["expires_in"] != float64(900) {
		t.Fatalf("result %v", result)
	}
	if _, has := result["result_key"]; has {
		t.Error("the stored key leaked into the response")
	}

	res, err := http.Get(url)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(res.Body)
	body := string(raw)

	if !strings.HasPrefix(body, "\xef\xbb\xbf") {
		t.Error("no UTF-8 BOM (BR-064)")
	}
	lines := strings.Split(strings.TrimRight(strings.TrimPrefix(body, "\xef\xbb\xbf"), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("%d CSV lines, want header + one per order line of the completed order: %q", len(lines), body)
	}
	if !strings.HasPrefix(lines[0], "order_number,") {
		t.Errorf("header %q", lines[0])
	}
	for _, l := range lines[1:] {
		if !strings.Contains(l, ",completed,") || !strings.Contains(l, sku) {
			t.Errorf("row %q", l)
		}
	}
	if !strings.Contains(lines[1], ",30000000,") || !strings.Contains(lines[1], ",10000000,") {
		t.Errorf("amounts are not plain integers: %q", lines[1])
	}
	if strings.Contains(body, "Dewi") {
		t.Error("a filtered-out order reached the export")
	}

	// BR-063: every read of the finished job signs a fresh working link.
	_, again := do("GET", "/v1/jobs/"+accepted["job_id"].(string), nil)
	url2, _ := again["result"].(map[string]any)["download_url"].(string)
	if url2 == "" {
		t.Fatal("second read lost the download_url")
	}
	res2, err := http.Get(url2)
	if err != nil || res2.StatusCode != 200 {
		t.Fatalf("regenerated link: %v %v", err, res2)
	}
	_ = res2.Body.Close()
}
