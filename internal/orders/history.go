package orders

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/db/sqlcgen"
)

// A shopper's own orders on the storefront (P1-208, 04-api-spec.md §11.9).

// HistoryCursor is the position after a page of a customer's orders.
type HistoryCursor struct {
	At time.Time `json:"t"`
	ID uuid.UUID `json:"i"`
}

// CustomerOrders is one customer's orders, newest first.
func (s *Service) CustomerOrders(ctx context.Context, customerID uuid.UUID, after *HistoryCursor, limit int) ([]sqlcgen.CustomerOrdersRow, *HistoryCursor, error) {
	p := sqlcgen.CustomerOrdersParams{CustomerID: &customerID, Lim: int32(limit + 1)}
	if after != nil {
		p.BeforeAt, p.BeforeID = &after.At, &after.ID
	}
	var rows []sqlcgen.CustomerOrdersRow
	err := s.tx(ctx, func(q *sqlcgen.Queries, _ pgx.Tx) (err error) {
		rows, err = q.CustomerOrders(ctx, p)
		return err
	})
	if err != nil || len(rows) <= limit {
		return rows, nil, err
	}
	rows = rows[:limit]
	last := rows[limit-1]
	return rows, &HistoryCursor{At: last.PlacedAt, ID: last.ID}, nil
}

// View is an order as a shopper sees it: the row, its lines and its newest
// Midtrans attempt, if any.
type View struct {
	Order  sqlcgen.Order
	Lines  []sqlcgen.OrderLine
	Latest *sqlcgen.Payment
}

// ByNumber is an order by order number, for the storefront to check against a
// customer or an order token. Not found is ErrNoSuchOrder, which each route
// answers in its own way.
func (s *Service) ByNumber(ctx context.Context, number string) (View, error) {
	var v View
	err := s.tx(ctx, func(q *sqlcgen.Queries, _ pgx.Tx) (err error) {
		if v.Order, err = q.OrderByNumber(ctx, number); errors.Is(err, pgx.ErrNoRows) {
			return ErrNoSuchOrder
		} else if err != nil {
			return err
		}
		if v.Lines, err = q.GetOrderLines(ctx, v.Order.ID); err != nil {
			return err
		}
		if p, err := q.LatestPayment(ctx, v.Order.ID); err == nil {
			v.Latest = &p
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		return nil
	})
	return v, err
}

// PaymentStatus is a Midtrans order's state for its shopper (§11.9): paid
// once the order is, whoever marked it; else the newest attempt's, with an
// amount mismatch still pending -- an operator decides; pending with no
// attempt. Bank transfer has none: the shop confirms it by hand.
func PaymentStatus(v View) *string {
	if v.Order.PaymentMethod != "midtrans" {
		return nil
	}
	st := "pending"
	switch {
	case v.Order.PaidAt != nil:
		st = "paid"
	case v.Latest != nil && (v.Latest.Status == "paid" || v.Latest.Status == "failed"):
		st = v.Latest.Status
	}
	return &st
}

// ErrNoSuchOrder is an order number with no order in this shop. Each route
// answers it its own way: 404 to its customer, 401 to a guest.
var ErrNoSuchOrder = errors.New("no such order")
