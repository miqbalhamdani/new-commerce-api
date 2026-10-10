package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/orders"
)

// Orders: 04-api-spec.md §5.1 (P1-103) and §5.2 (P1-104).

func (s *Server) GetOrder(w http.ResponseWriter, r *http.Request, id Id) {
	requirePermission(auth.PermOrdersRead, func(w http.ResponseWriter, r *http.Request) {
		s.writeOrderDetail(w, r, id)
	})(w, r)
}

type orderUpdateBody struct {
	ShippingAddress optional[ShippingAddressWrite] `json:"shipping_address"`
	Note            optional[string]               `json:"note"`
	Shipping        optional[int64]                `json:"shipping"`
}

// UpdateOrder is the §5.2 PATCH: address, note and shipping, only while the
// order is pending, at the version in If-Match (BR-010, BR-079).
func (s *Server) UpdateOrder(w http.ResponseWriter, r *http.Request, id Id, params UpdateOrderParams) {
	requirePermission(auth.PermOrdersWrite, func(w http.ResponseWriter, r *http.Request) {
		var body orderUpdateBody
		// status changes only through the transition routes (BR-071), so it is
		// refused like a server-managed field here.
		if !decodeJSON(w, r, &body, "status") {
			return
		}
		if err := rejectNull(map[string]bool{"shipping_address": body.ShippingAddress.Null,
			"shipping": body.Shipping.Null}); err != nil {
			writeError(w, r, err)
			return
		}
		var in orders.UpdateInput
		if body.ShippingAddress.Set {
			raw, err := json.Marshal(body.ShippingAddress.Value)
			if err != nil {
				writeError(w, r, err)
				return
			}
			in.ShippingAddress = raw
		}
		if body.Note.Set {
			if body.Note.Null {
				in.ClearNote = true
			} else {
				in.Note = &body.Note.Value
			}
		}
		if body.Shipping.Set {
			in.Shipping = &body.Shipping.Value
		}
		if _, err := s.orders.UpdatePending(r.Context(), id, params.IfMatch, in); err != nil {
			writeError(w, r, err)
			return
		}
		s.writeOrderDetail(w, r, id)
	})(w, r)
}

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
