package httpapi

import (
	"net/http"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/orders"
)

// Orders: 04-api-spec.md §5.1 (P1-103).

func (s *Server) ListOrders(w http.ResponseWriter, r *http.Request, params ListOrdersParams) {
	requirePermission(auth.PermOrdersRead, func(w http.ResponseWriter, r *http.Request) {
		f := orders.Filter{CustomerID: params.CustomerId, RefundOwed: params.RefundOwed,
			Q: params.Q, Limit: pageLimit(params.Limit)}
		if params.Status != nil {
			for _, st := range *params.Status {
				if !st.Valid() {
					writeError(w, r, fieldErr("status", "status is pending, paid, processing, shipped, completed or cancelled"))
					return
				}
				f.Status = append(f.Status, string(st))
			}
		}
		if params.Source != nil {
			if !params.Source.Valid() {
				writeError(w, r, fieldErr("source", "source is storefront or manual"))
				return
			}
			src := string(*params.Source)
			f.Source = &src
		}
		if params.Sort != nil {
			if !params.Sort.Valid() {
				writeError(w, r, fieldErr("sort", "sort is -placed_at or placed_at"))
				return
			}
			f.Sort = string(*params.Sort)
		}
		var err error
		if f.PlacedFrom, err = timeParam("placed_from", params.PlacedFrom); err != nil {
			writeError(w, r, err)
			return
		}
		if f.PlacedTo, err = timeParam("placed_to", params.PlacedTo); err != nil {
			writeError(w, r, err)
			return
		}
		if params.Cursor != nil {
			f.After = &orders.Cursor{}
			if err := decodeCursor(params.Cursor, f.After); err != nil {
				writeError(w, r, err)
				return
			}
		}
		rows, next, err := s.orders.List(r.Context(), f)
		if err != nil {
			writeError(w, r, err)
			return
		}
		page := OrderPage{Data: make([]OrderListRow, 0, len(rows))}
		for _, o := range rows {
			page.Data = append(page.Data, orderRowOut(o))
		}
		if next != nil {
			page.NextCursor = encodeCursor(next)
		}
		writeJSON(w, http.StatusOK, page)
	})(w, r)
}

func orderRowOut(o orders.Row) OrderListRow {
	return OrderListRow{Id: o.ID, OrderNumber: o.OrderNumber, Source: OrderSource(o.Source),
		Status: OrderStatus(o.Status), Version: o.Version,
		Customer: jsonbOut[OrderCustomerSnapshot](o.Customer), ItemCount: o.ItemCount,
		Total: o.Total, PlacedAt: o.PlacedAt, PaidAt: o.PaidAt, RefundedAt: o.RefundedAt}
}
