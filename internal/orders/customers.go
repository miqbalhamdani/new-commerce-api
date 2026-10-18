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

// CustomerList is one page of customers: the rows, the keyset position after
// them (nil on the last page), and how many customers match in all.
type CustomerList struct {
	Rows  []sqlcgen.ListCustomersRow
	Next  *CustomerCursor
	Total int64
}

// ListCustomers is GET /v1/customers (P1-106): q over name, email and phone,
// newest first on the time-ordered v7 id. page (1-based) pages by offset for a
// numbered pager instead of by keyset; the total is counted either way (P1-231).
func (s *Service) ListCustomers(ctx context.Context, q *string, after *CustomerCursor, page, limit int) (CustomerList, error) {
	pattern := likePattern(q)
	params := sqlcgen.ListCustomersParams{Q: pattern, Lim: int32(limit + 1)}
	if page > 0 {
		params.Off = int32((page - 1) * limit)
	} else if after != nil {
		params.BeforeID = &after.ID
	}
	var (
		out   []sqlcgen.ListCustomersRow
		total int64
	)
	err := s.tx(ctx, func(qs *sqlcgen.Queries, _ pgx.Tx) error {
		var err error
		if total, err = qs.CountCustomers(ctx, pattern); err != nil {
			return err
		}
		out, err = qs.ListCustomers(ctx, params)
		return err
	})
	if err != nil {
		return CustomerList{}, err
	}
	if len(out) <= limit {
		return CustomerList{Rows: out, Total: total}, nil
	}
	out = out[:limit]
	return CustomerList{Rows: out, Next: &CustomerCursor{ID: out[limit-1].ID}, Total: total}, nil
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
	list, err := s.List(ctx, Filter{CustomerID: &id, Limit: 50})
	return c, list.Rows, err
}
