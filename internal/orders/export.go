package orders

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/db/sqlcgen"
	"github.com/miqbalhamdani/new-commerce-api/internal/jobs"
)

// StartExport queues an order_export job carrying the §5.1 filters as its
// params (BR-065). The job row and its audit entry commit together; the
// message is enqueued only after (BR-060).
func (s *Service) StartExport(ctx context.Context, f Filter) (uuid.UUID, error) {
	id := uuid.Must(uuid.NewV7())
	var job jobs.Job
	err := s.tx(ctx, func(_ *sqlcgen.Queries, tx pgx.Tx) error {
		var err error
		job, err = jobs.CreateWithID(ctx, tx, id, "order_export", f, actorOf(ctx))
		if err != nil {
			return err
		}
		return db.Audit(ctx, tx, db.AuditEntry{Action: "order.export", SubjectType: "job",
			SubjectID: id.String(), After: f})
	})
	if err != nil {
		return uuid.Nil, err
	}
	return id, s.jobs.Enqueue(ctx, job)
}

// exportHeader is the CSV's first row: order fields repeated per line, then
// the line itself (BR-065). Amounts are plain integer minor units so a
// comma-decimal locale cannot corrupt them (BR-064).
var exportHeader = []string{
	"order_number", "source", "status", "placed_at", "paid_at", "shipped_at", "completed_at",
	"cancelled_at", "refunded_at", "payment_method", "customer_name", "customer_email",
	"customer_phone", "address_line1", "address_line2", "address_city", "address_province",
	"address_postal_code", "courier", "tracking_number", "order_subtotal", "order_shipping",
	"order_discount", "order_total", "line_sku", "line_title", "line_qty", "line_unit_price",
	"line_discount", "line_total",
}

// ExportHandler is the worker half: one CSV row per order line with the order
// fields repeated, a UTF-8 BOM for Excel (BR-064), written to
// exports/{tenant}/{job}.csv -- the key the lifecycle rule expires after 7
// days (BR-053). Safe to run twice: the same job writes the same key.
func (s *Service) ExportHandler() jobs.Handler {
	return func(ctx context.Context, job jobs.Job, progress jobs.Progress) (any, error) {
		var f Filter
		if len(job.Params) > 0 {
			if err := json.Unmarshal(job.Params, &f); err != nil {
				return nil, err
			}
		}
		a := &sqlArgs{}
		where := filterSQL(f, a)
		sql := `SELECT o.order_number, o.source, o.status,
		       o.placed_at, o.paid_at, o.shipped_at, o.completed_at, o.cancelled_at, o.refunded_at,
		       o.payment_method,
		       o.customer->>'name', o.customer->>'email', o.customer->>'phone',
		       o.shipping_address->>'line1', o.shipping_address->>'line2', o.shipping_address->>'city',
		       o.shipping_address->>'province', o.shipping_address->>'postal_code',
		       o.courier, o.tracking_number,
		       o.subtotal_amount, o.shipping_amount, o.discount_amount, o.total_amount,
		       l.sku_snapshot, l.title_snapshot, l.qty, l.unit_price, l.discount_amount
		FROM orders o JOIN order_lines l ON l.order_id = o.id
		WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY o.placed_at DESC, o.id, l.id`

		var buf bytes.Buffer
		buf.WriteString("\xef\xbb\xbf") // BOM: Excel assumes a legacy code page without it (BR-064)
		w := csv.NewWriter(&buf)
		if err := w.Write(exportHeader); err != nil {
			return nil, err
		}
		n := 0
		err := s.tx(ctx, func(_ *sqlcgen.Queries, tx pgx.Tx) error {
			rows, err := tx.Query(ctx, sql, a.args...)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var (
					number, source, status, payment              string
					placed                                       time.Time
					paid, shipped, completed, cancelled, refund  *time.Time
					name, email, phone                           *string
					line1, line2, city, province, postal         *string
					courier, tracking                            *string
					subtotal, shippingAmt, discountAmt, totalAmt int64
					sku, title                                   string
					qty                                          int32
					unitPrice, lineDiscount                      int64
				)
				if err := rows.Scan(&number, &source, &status, &placed, &paid, &shipped, &completed,
					&cancelled, &refund, &payment, &name, &email, &phone, &line1, &line2, &city,
					&province, &postal, &courier, &tracking, &subtotal, &shippingAmt, &discountAmt,
					&totalAmt, &sku, &title, &qty, &unitPrice, &lineDiscount); err != nil {
					return err
				}
				rec := []string{
					number, source, status, placed.Format(time.RFC3339), csvTime(paid),
					csvTime(shipped), csvTime(completed), csvTime(cancelled), csvTime(refund),
					payment, deref(name), deref(email), deref(phone), deref(line1), deref(line2),
					deref(city), deref(province), deref(postal), deref(courier), deref(tracking),
					csvInt(subtotal), csvInt(shippingAmt), csvInt(discountAmt), csvInt(totalAmt),
					sku, title, strconv.Itoa(int(qty)), csvInt(unitPrice), csvInt(lineDiscount),
					csvInt(unitPrice*int64(qty) - lineDiscount),
				}
				if err := w.Write(rec); err != nil {
					return err
				}
				n++
			}
			return rows.Err()
		})
		if err != nil {
			return nil, err
		}
		w.Flush()
		if err := w.Error(); err != nil {
			return nil, err
		}

		key := fmt.Sprintf("exports/%s/%s.csv", tenantOf(ctx), job.ID)
		if err := s.files.Put(ctx, key, &buf, int64(buf.Len()), "text/csv"); err != nil {
			return nil, err
		}
		if err := progress(n, &n, 0); err != nil {
			return nil, err
		}
		return map[string]any{"result_key": key, "rows": n}, nil
	}
}

func csvTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format(time.RFC3339)
}

func csvInt(v int64) string {
	return strconv.FormatInt(v, 10)
}
