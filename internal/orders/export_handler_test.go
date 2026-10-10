package orders_test

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/db/sqlcgen"
	"github.com/miqbalhamdani/new-commerce-api/internal/orders"
	"github.com/miqbalhamdani/new-commerce-api/internal/storage"
)

// TestExportHandlerDirect runs the export handler without the stream, so an
// orphaned worker on the shared Redis cannot eat the job (see P1-101's commit
// note). The HTTP round trip is TestOrderExport.
func TestExportHandlerDirect(t *testing.T) {
	h := newHarness(t)
	files, err := storage.FromEnv()
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	svc := orders.NewService(h.store, files, nil)

	courier := "jne"
	id := h.seedOrder(t, "completed", &courier)
	// Two lines on the completed order; a pending one that the filter drops.
	productID, variantID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	if err := h.store.InTenantTx(h.ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(h.ctx, `INSERT INTO products (id, tenant_id, title, slug) VALUES ($1, $2, 'Exp Tee', $3)`, productID, h.tenant, "exp-"+productID.String()); err != nil {
			return err
		}
		if _, err := tx.Exec(h.ctx, `INSERT INTO variants (id, tenant_id, product_id, sku, regular_price_amount) VALUES ($1, $2, $3, 'EXPD-1', 10000000)`, variantID, h.tenant, productID); err != nil {
			return err
		}
		for _, qty := range []int{2, 1} {
			if _, err := tx.Exec(h.ctx, `INSERT INTO order_lines (id, tenant_id, order_id, variant_id, sku_snapshot, title_snapshot, qty, unit_price)
				VALUES ($1, $2, $3, $4, 'EXPD-1', 'Exp Tee', $5, 10000000)`, uuid.Must(uuid.NewV7()), h.tenant, id, variantID, qty); err != nil {
				return err
			}
		}
		_, err := tx.Exec(h.ctx, `UPDATE orders SET subtotal_amount = 30000000, total_amount = 30000000 WHERE id = $1`, id)
		return err
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	params, _ := json.Marshal(orders.Filter{Status: []string{"completed"}})
	jobID := uuid.Must(uuid.NewV7())
	var total *int
	result, err := svc.ExportHandler()(h.ctx, sqlcgen.Job{ID: jobID, Kind: "order_export", Params: params},
		func(processed int, t *int, failed int) error { total = t; return nil })
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	m := result.(map[string]any)
	key := m["result_key"].(string)
	if !strings.HasPrefix(key, "exports/"+h.tenant.String()+"/") || m["rows"] != 2 || total == nil || *total != 2 {
		t.Fatalf("result %v, progress total %v", m, total)
	}

	obj, err := files.Get(context.WithoutCancel(h.ctx), key)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	defer func() { _ = obj.Close() }()
	raw, err := io.ReadAll(obj)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	body := string(raw)
	if !strings.HasPrefix(body, "\xef\xbb\xbf") {
		t.Error("no UTF-8 BOM (BR-064)")
	}
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("%d lines, want header + 2: %q", len(lines), body)
	}
	if !strings.Contains(lines[1], ",30000000,") || !strings.Contains(lines[1], ",2,") {
		t.Errorf("row %q", lines[1])
	}
}
