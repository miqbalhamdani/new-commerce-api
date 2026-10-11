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

// ByNumber is an order and its lines by order number, for the storefront to
// check against a customer or an order token. Not found is ErrNoSuchOrder,
// which each route answers in its own way.
func (s *Service) ByNumber(ctx context.Context, number string) (sqlcgen.Order, []sqlcgen.OrderLine, error) {
	var (
		o     sqlcgen.Order
		lines []sqlcgen.OrderLine
	)
	err := s.tx(ctx, func(q *sqlcgen.Queries, _ pgx.Tx) (err error) {
		if o, err = q.OrderByNumber(ctx, number); errors.Is(err, pgx.ErrNoRows) {
			return ErrNoSuchOrder
		} else if err != nil {
			return err
		}
		lines, err = q.GetOrderLines(ctx, o.ID)
		return err
	})
	return o, lines, err
}

// ErrNoSuchOrder is an order number with no order in this shop. Each route
// answers it its own way: 404 to its customer, 401 to a guest.
var ErrNoSuchOrder = errors.New("no such order")
