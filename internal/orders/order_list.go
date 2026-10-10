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
	Status     []string   `json:"status,omitempty"`
	Source     *string    `json:"source,omitempty"`
	CustomerID *uuid.UUID `json:"customer_id,omitempty"`
	RefundOwed *bool      `json:"refund_owed,omitempty"`
	PlacedFrom *time.Time `json:"placed_from,omitempty"`
	PlacedTo   *time.Time `json:"placed_to,omitempty"`
	Q          *string    `json:"q,omitempty"`
	// The export job stores the fields above as its params; the three below
	// are the list's own.
	Sort  string  `json:"-"` // "-placed_at" (default) or "placed_at"
	After *Cursor `json:"-"`
	Limit int     `json:"-"`
}

// sqlArgs numbers placeholders as it collects their values.
type sqlArgs struct{ args []any }

func (a *sqlArgs) add(v any) string {
	a.args = append(a.args, v)
	return fmt.Sprintf("$%d", len(a.args))
}

// filterSQL renders the §5.1 filters as WHERE clauses over alias o, shared by
// the list and the export job (BR-065).
func filterSQL(f Filter, a *sqlArgs) []string {
	where := []string{"true"}
	if len(f.Status) > 0 {
		where = append(where, "o.status = ANY("+a.add(f.Status)+")")
	}
	if f.Source != nil {
		where = append(where, "o.source = "+a.add(*f.Source))
	}
	if f.CustomerID != nil {
		where = append(where, "o.customer_id = "+a.add(*f.CustomerID))
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
		where = append(where, "o.placed_at >= "+a.add(*f.PlacedFrom))
	}
	if f.PlacedTo != nil {
		where = append(where, "o.placed_at < "+a.add(*f.PlacedTo))
	}
	if q := likePattern(f.Q); q != nil {
		p := a.add(*q)
		where = append(where, "(o.order_number ILIKE '%' || "+p+" || '%'"+
			" OR o.customer->>'name' ILIKE '%' || "+p+" || '%'"+
			" OR o.customer->>'email' ILIKE '%' || "+p+" || '%'"+
			" OR o.customer->>'phone' ILIKE '%' || "+p+" || '%')")
	}
	return where
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
	a := &sqlArgs{}
	where := filterSQL(f, a)

	order := "o.placed_at DESC, o.id DESC"
	if f.Sort == "placed_at" {
		order = "o.placed_at, o.id"
		if f.After != nil {
			where = append(where, "(o.placed_at, o.id) > ("+a.add(f.After.At)+", "+a.add(f.After.ID)+")")
		}
	} else if f.After != nil {
		where = append(where, "(o.placed_at, o.id) < ("+a.add(f.After.At)+", "+a.add(f.After.ID)+")")
	}

	// The page is chosen first; the line aggregate then runs for those rows
	// only, not for every row the filter matches.
	sql := `WITH page AS (
	  SELECT o.* FROM orders o
	   WHERE ` + strings.Join(where, " AND ") + `
	   ORDER BY ` + order + `
	   LIMIT ` + a.add(f.Limit+1) + `)
	SELECT o.id, o.order_number, o.source, o.status, o.version, o.customer,
	       coalesce(l.n, 0), o.total_amount, o.placed_at, o.paid_at, o.refunded_at
	FROM page o
	LEFT JOIN LATERAL (SELECT sum(x.qty)::int AS n FROM order_lines x WHERE x.order_id = o.id) l ON true
	ORDER BY ` + order

	var out []Row
	err := s.tx(ctx, func(_ *sqlcgen.Queries, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, sql, a.args...)
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
