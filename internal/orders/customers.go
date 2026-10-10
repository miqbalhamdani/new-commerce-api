package orders

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/db/sqlcgen"
)

// CustomerCursor is the keyset position after a page of customers.
type CustomerCursor struct {
	ID uuid.UUID `json:"i"`
}

// ListCustomers is GET /v1/customers (P1-106): q over name, email and phone,
// newest first on the time-ordered v7 id.
func (s *Service) ListCustomers(ctx context.Context, q *string, after *CustomerCursor, limit int) ([]sqlcgen.ListCustomersRow, *CustomerCursor, error) {
	params := sqlcgen.ListCustomersParams{Q: likePattern(q), Lim: int32(limit + 1)}
	if after != nil {
		params.BeforeID = &after.ID
	}
	var out []sqlcgen.ListCustomersRow
	err := s.tx(ctx, func(qs *sqlcgen.Queries, _ pgx.Tx) error {
		var err error
		out, err = qs.ListCustomers(ctx, params)
		return err
	})
	if err != nil || len(out) <= limit {
		return out, nil, err
	}
	out = out[:limit]
	return out, &CustomerCursor{ID: out[limit-1].ID}, nil
}

// GetCustomer is the §5.5 detail: the profile plus up to 50 order-list rows,
// newest first. Never a credential field (BR-092).
func (s *Service) GetCustomer(ctx context.Context, id uuid.UUID) (sqlcgen.GetCustomerRow, []Row, error) {
	var c sqlcgen.GetCustomerRow
	err := s.tx(ctx, func(qs *sqlcgen.Queries, _ pgx.Tx) error {
		var err error
		c, err = qs.GetCustomer(ctx, id)
		return notFound(err, "customer")
	})
	if err != nil {
		return c, nil, err
	}
	rows, _, err := s.List(ctx, Filter{CustomerID: &id, Limit: 50})
	return c, rows, err
}
