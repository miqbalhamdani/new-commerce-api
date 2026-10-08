package httpapi_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/catalog"
	"github.com/miqbalhamdani/new-commerce-api/internal/images"
	"github.com/miqbalhamdani/new-commerce-api/internal/jobs"
)

func init() {
	isolationCases = append(isolationCases, isolationCase{
		route: route{"POST", "/v1/products/import"}, seed: seedProduct,
		request: func(t *testing.T, s seeded) *http.Request {
			// Another tenant's key prefix is refused before anything is read.
			return bodyRequest(t, http.MethodPost, "/v1/products/import", s.accessToken, map[string]any{
				"r2_key":         "jobs/" + uuid.NewString() + "/" + uuid.NewString() + "/upload.csv",
				"column_mapping": map[string]string{"Title": "title"}, "on_conflict": "update"})
		}})
	auditCases = append(auditCases, auditCase{
		route: route{"POST", "/v1/products/import"},
		request: func(t *testing.T, s seeded) (*http.Request, string, string) {
			key := uploadCSV(t, s, []byte("Title\nAudited Tee\n"))
			return bodyRequest(t, http.MethodPost, "/v1/products/import", s.accessToken, map[string]any{
				"r2_key": key, "column_mapping": map[string]string{"Title": "title"}, "on_conflict": "update"}), "job", jobOfKey(key)
		}})
}

// TestImport is P1-073's behaviour: ';' and a BOM handled, rows grouped
// into products with options, an existing SKU updated, and every failed row
// in errors.csv with its original line number (BR-044).
func TestImport(t *testing.T) {
	ctx := t.Context()
	store := openAppStore(ctx, t)
	admin := seedSignedInUserWithRole(ctx, t, store, uuid.Must(uuid.NewV7()), auth.RoleAdmin)
	do := apiClient(t, admin)

	existing := apiCreate(t, admin, "/v1/products", map[string]any{"title": "Old Tee"})
	apiCreate(t, admin, "/v1/products/"+existing+"/variants", map[string]any{"option_values": []string{}, "sku": "OLD-1"})

	file := "\xef\xbb\xbfNama Produk;SKU;Harga;Warna;Ukuran;Catatan\n" +
		"Kaos Basic;KB-HTM-S;199.000;Hitam;S;a\n" + // line 2
		"Kaos Basic;KB-HTM-M;199.000;Hitam;M;b\n" + // line 3
		"Kemeja;KM-1;299000;Biru;L;c\n" + // line 4
		"Kaos Basic;KB-PTH-S;harga?;Putih;S;d\n" + // line 5: bad price
		";OLD-1;150000;;;e\n" + // line 6: update an existing SKU
		";NOPE-1;1;;;f\n" // line 7: new SKU without a title
	key := uploadCSV(t, admin, []byte(file))
	code, p := do("POST", "/v1/products/import", map[string]any{"r2_key": key, "on_conflict": "update",
		"column_mapping": map[string]string{"Nama Produk": "title", "SKU": "sku", "Harga": "regular_price",
			"Warna": "option:Colour", "Ukuran": "option:Size"}})
	if code != 202 || p["job_id"] != jobOfKey(key) {
		t.Fatalf("import: %d %v", code, p)
	}
	job := waitForJob(t, admin, p["job_id"].(string))
	result := job["result"].(map[string]any)
	if job["state"] != "done" || result["created"] != float64(3) || result["updated"] != float64(1) || job["failed"] != float64(2) {
		t.Fatalf("job %v", job)
	}

	res, err := http.Get(result["error_report_url"].(string))
	if err != nil {
		t.Fatalf("download errors.csv: %v", err)
	}
	report, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if !bytes.HasPrefix(report, []byte("\xef\xbb\xbfline,error,Nama Produk")) ||
		!strings.Contains(string(report), "\n5,") || !strings.Contains(string(report), "\n7,") {
		t.Errorf("errors.csv:\n%s", report)
	}

	_, list := do("GET", "/v1/products?q=Kaos+Basic", nil)
	row := list["data"].([]any)[0].(map[string]any)
	if row["variant_count"] != float64(2) || row["price_min"] != float64(19900000) {
		t.Errorf("Kaos Basic: %v", row)
	}
	_, prod := do("GET", "/v1/products/"+row["id"].(string), nil)
	if names := prod["option_names"].([]any); len(names) != 2 || names[0] != "Colour" {
		t.Errorf("option names %v", names)
	}

	t.Run("a mapping must name title or sku, and real fields", func(t *testing.T) {
		code, p := do("POST", "/v1/products/import", map[string]any{"r2_key": key, "on_conflict": "update",
			"column_mapping": map[string]string{"Harga": "price"}})
		assertProblem(t, code, p, 422, "validation_failed", "column_mapping")
	})
	t.Run("the same upload twice is refused", func(t *testing.T) {
		code, p := do("POST", "/v1/products/import", map[string]any{"r2_key": key, "on_conflict": "update",
			"column_mapping": map[string]string{"Nama Produk": "title"}})
		assertProblem(t, code, p, 422, "validation_failed", "r2_key")
	})
}

// TestImport10k is the phase exit (P1-073): 10,000 variants in under five
// minutes.
func TestImport10k(t *testing.T) {
	if testing.Short() {
		t.Skip("imports 10,000 variants")
	}
	ctx := t.Context()
	store := openAppStore(ctx, t)
	admin := seedSignedInUserWithRole(ctx, t, store, uuid.Must(uuid.NewV7()), auth.RoleAdmin)

	var b strings.Builder
	b.WriteString("Title,SKU,Price,Weight,Size\n")
	sizes := []string{"S", "M", "L", "XL", "XXL"}
	for p := 0; p < 2000; p++ {
		for _, s := range sizes {
			fmt.Fprintf(&b, "Tee %d,T%05d-%s,199000,200,%s\n", p, p, s, s)
		}
	}
	key := uploadCSV(t, admin, []byte(b.String()))
	start := time.Now()
	code, p := apiClient(t, admin)("POST", "/v1/products/import", map[string]any{"r2_key": key, "on_conflict": "update",
		"column_mapping": map[string]string{"Title": "title", "SKU": "sku", "Price": "regular_price",
			"Weight": "weight_grams", "Size": "option:Size"}})
	if code != 202 {
		t.Fatalf("import: %d %v", code, p)
	}
	job := waitForJob(t, admin, p["job_id"].(string))
	took := time.Since(start)
	if job["state"] != "done" || job["result"].(map[string]any)["created"] != float64(10000) || job["failed"] != float64(0) {
		t.Fatalf("job %v", job)
	}
	t.Logf("10,000 variants imported in %s", took)
	if took > 5*time.Minute {
		t.Errorf("took %s, want under 5 minutes", took)
	}
}

// TestImportResumes: a delivery after the last batch committed applies
// nothing again -- the at-least-once redelivery of BR-060 adds no rows.
func TestImportResumes(t *testing.T) {
	ctx := t.Context()
	store := openAppStore(ctx, t)
	tenantID := uuid.Must(uuid.NewV7())
	admin := seedSignedInUserWithRole(ctx, t, store, tenantID, auth.RoleAdmin)
	key := uploadCSV(t, admin, []byte("Title,Size\nResume Tee,S\nResume Tee,M\n"))
	_, p := apiClient(t, admin)("POST", "/v1/products/import", map[string]any{"r2_key": key, "on_conflict": "update",
		"column_mapping": map[string]string{"Title": "title", "Size": "option:Size"}})
	waitForJob(t, admin, p["job_id"].(string))

	// Deliver it again, straight to the handler, as a reclaimed message would.
	svc := catalog.NewService(store, testFiles(t), jobs.NewService(store, testRedis(t)))
	tctx := tenantCtx(ctx, tenantID)
	j, err := jobs.NewService(store, testRedis(t)).Get(tctx, uuid.MustParse(p["job_id"].(string)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ImportHandler()(tctx, j, func(int, *int, int) error { return nil }); err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	_, list := apiClient(t, admin)("GET", "/v1/products?q=Resume", nil)
	if n := len(list["data"].([]any)); n != 1 {
		t.Errorf("%d products after a redelivery, want 1", n)
	}
}

// uploadCSV presigns an import and PUTs the file to the store.
func uploadCSV(t *testing.T, s seeded, body []byte) string {
	t.Helper()
	sum := sha256.Sum256(body)
	code, p := apiClient(t, s)("POST", "/v1/media/presign", map[string]any{"purpose": "product_import",
		"mime_type": "text/csv", "bytes": len(body), "sha256": hex.EncodeToString(sum[:])})
	if code != 200 {
		t.Fatalf("presign: %d %v", code, p)
	}
	req, _ := http.NewRequest(http.MethodPut, p["upload_url"].(string), bytes.NewReader(body))
	req.Header.Set("Content-Type", "text/csv")
	res, err := http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("PUT csv: %v %v", err, res)
	}
	_ = res.Body.Close()
	return p["r2_key"].(string)
}

func jobOfKey(key string) string { return strings.Split(key, "/")[2] }

// waitForJob runs a worker in-process until the job finishes, then returns
// GET /v1/jobs/{id}.
func waitForJob(t *testing.T, s seeded, id string) map[string]any {
	t.Helper()
	store, redis := openAppStore(t.Context(), t), testRedis(t)
	svc := catalog.NewService(store, testFiles(t), jobs.NewService(store, redis))
	runner := &jobs.Runner{Store: store, Queue: redis, Consumer: "test-" + id, ClaimIdle: 2 * time.Second,
		MaxDeliveries: 2, Handlers: map[string]jobs.Handler{"product_import": svc.ImportHandler(),
			"image_derivatives": images.Handler(store, testFiles(t))}}
	ctx, stop := context.WithCancel(t.Context())
	defer stop()
	go func() { _ = runner.Run(ctx) }()

	deadline := time.Now().Add(5 * time.Minute)
	for {
		_, j := apiClient(t, s)("GET", "/v1/jobs/"+id, nil)
		if j["state"] == "done" || j["state"] == "failed" {
			return j
		}
		if time.Now().After(deadline) {
			t.Fatalf("job never finished: %v", j)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
