package orders

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/db/sqlcgen"
)

// Filter is GET /v1/orders (04-api-spec.md §5.1). The admin's saved views are
// plain filters: To confirm payment status=[pending] · To ship
// status=[paid, processing] · Shipped status=[shipped] · Cancelled, refund
// owed refund_owed=true (BR-075).
type Filter struct {
	Status     []string
	Source     *string
	CustomerID *uuid.UUID
	RefundOwed *bool
	PlacedFrom *time.Time
	PlacedTo   *time.Time
	Q          *string
	Sort       string // "-placed_at" (default) or "placed_at"
	After      *Cursor
	Limit      int
}

// Cursor is the keyset position after a page: placed_at plus the id that
// breaks ties.
type Cursor struct {
	At time.Time `json:"t"`
	ID uuid.UUID `json:"i"`
}

// Row is one §5.1 list row; item_count is the summed qty over the lines.
type Row struct {
	ID          uuid.UUID
	OrderNumber string
	Source      string
	Status      string
	Version     int
	Customer    []byte // the {name, email, phone} snapshot, verbatim
	ItemCount   int
	Total       int64
	PlacedAt    time.Time
	PaidAt      *time.Time
	RefundedAt  *time.Time
}

// List is the order list (P1-103). Hand-written rather than sqlc: the sort
// decides both ORDER BY and the keyset predicate. q is a plain ILIKE over the
// order number and the customer snapshot -- no index on purpose (03-erd.md
// §3.6); the saved views ride the three orders indexes, refund_owed its
// partial one.
func (s *Service) List(ctx context.Context, f Filter) ([]Row, *Cursor, error) {
	var (
		where = []string{"true"}
		args  []any
	)
	arg := func(v any) string { args = append(args, v); return fmt.Sprintf("$%d", len(args)) }

	if len(f.Status) > 0 {
		where = append(where, "o.status = ANY("+arg(f.Status)+")")
	}
	if f.Source != nil {
		where = append(where, "o.source = "+arg(*f.Source))
	}
	if f.CustomerID != nil {
		where = append(where, "o.customer_id = "+arg(*f.CustomerID))
	}
	if f.RefundOwed != nil {
		// Exactly the partial index's predicate (BR-075).
		owed := "(o.status = 'cancelled' AND o.paid_at IS NOT NULL AND o.refunded_at IS NULL)"
		if !*f.RefundOwed {
			owed = "NOT " + owed
		}
		where = append(where, owed)
	}
	if f.PlacedFrom != nil {
		where = append(where, "o.placed_at >= "+arg(*f.PlacedFrom))
	}
	if f.PlacedTo != nil {
		where = append(where, "o.placed_at < "+arg(*f.PlacedTo))
	}
	if q := likePattern(f.Q); q != nil {
		p := arg(*q)
		where = append(where, "(o.order_number ILIKE '%' || "+p+" || '%'"+
			" OR o.customer->>'name' ILIKE '%' || "+p+" || '%'"+
			" OR o.customer->>'email' ILIKE '%' || "+p+" || '%'"+
			" OR o.customer->>'phone' ILIKE '%' || "+p+" || '%')")
	}

	order := "o.placed_at DESC, o.id DESC"
	if f.Sort == "placed_at" {
		order = "o.placed_at, o.id"
		if f.After != nil {
			where = append(where, "(o.placed_at, o.id) > ("+arg(f.After.At)+", "+arg(f.After.ID)+")")
		}
	} else if f.After != nil {
		where = append(where, "(o.placed_at, o.id) < ("+arg(f.After.At)+", "+arg(f.After.ID)+")")
	}

	// The page is chosen first; the line aggregate then runs for those rows
	// only, not for every row the filter matches.
	sql := `WITH page AS (
	  SELECT o.* FROM orders o
	   WHERE ` + strings.Join(where, " AND ") + `
	   ORDER BY ` + order + `
	   LIMIT ` + arg(f.Limit+1) + `)
	SELECT o.id, o.order_number, o.source, o.status, o.version, o.customer,
	       coalesce(l.n, 0), o.total_amount, o.placed_at, o.paid_at, o.refunded_at
	FROM page o
	LEFT JOIN LATERAL (SELECT sum(x.qty)::int AS n FROM order_lines x WHERE x.order_id = o.id) l ON true
	ORDER BY ` + order

	var out []Row
	err := s.tx(ctx, func(_ *sqlcgen.Queries, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r Row
			if err := rows.Scan(&r.ID, &r.OrderNumber, &r.Source, &r.Status, &r.Version, &r.Customer,
				&r.ItemCount, &r.Total, &r.PlacedAt, &r.PaidAt, &r.RefundedAt); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil || len(out) <= f.Limit {
		return out, nil, err
	}
	out = out[:f.Limit]
	last := out[f.Limit-1]
	return out, &Cursor{At: last.PlacedAt, ID: last.ID}, nil
}
