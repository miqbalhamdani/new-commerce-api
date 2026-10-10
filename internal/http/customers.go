package httpapi

import (
	"net/http"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/db/sqlcgen"
	"github.com/miqbalhamdani/new-commerce-api/internal/orders"
)

// Customers: 04-api-spec.md §5.5 (P1-106). Read-only (BR-092).

func (s *Server) ListCustomers(w http.ResponseWriter, r *http.Request, params ListCustomersParams) {
	requirePermission(auth.PermCustomersRead, func(w http.ResponseWriter, r *http.Request) {
		var after *orders.CustomerCursor
		if params.Cursor != nil {
			after = &orders.CustomerCursor{}
			if err := decodeCursor(params.Cursor, after); err != nil {
				writeError(w, r, err)
				return
			}
		}
		rows, next, err := s.orders.ListCustomers(r.Context(), params.Q, after, pageLimit(params.Limit))
		if err != nil {
			writeError(w, r, err)
			return
		}
		page := CustomerPage{Data: make([]Customer, 0, len(rows))}
		for _, c := range rows {
			page.Data = append(page.Data, customerOut(c))
		}
		if next != nil {
			page.NextCursor = encodeCursor(next)
		}
		writeJSON(w, http.StatusOK, page)
	})(w, r)
}

func (s *Server) GetCustomer(w http.ResponseWriter, r *http.Request, id Id) {
	requirePermission(auth.PermCustomersRead, func(w http.ResponseWriter, r *http.Request) {
		c, rows, err := s.orders.GetCustomer(r.Context(), id)
		if err != nil {
			writeError(w, r, err)
			return
		}
		out := CustomerDetail{Id: c.ID, Name: c.Name, Email: c.Email, Phone: c.Phone,
			OrderCount: int(c.OrderCount), CreatedAt: c.CreatedAt,
			Orders: make([]OrderListRow, 0, len(rows))}
		for _, o := range rows {
			out.Orders = append(out.Orders, orderRowOut(o))
		}
		writeJSON(w, http.StatusOK, out)
	})(w, r)
}

func customerOut(c sqlcgen.ListCustomersRow) Customer {
	return Customer{Id: c.ID, Name: c.Name, Email: c.Email, Phone: c.Phone,
		OrderCount: int(c.OrderCount), CreatedAt: c.CreatedAt}
}
