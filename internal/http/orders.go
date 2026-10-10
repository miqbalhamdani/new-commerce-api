package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/google/uuid"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/orders"
)

// Orders: 04-api-spec.md §5.1 (P1-103) and §5.2 (P1-104).

func (s *Server) GetOrder(w http.ResponseWriter, r *http.Request, id Id) {
	requirePermission(auth.PermOrdersRead, func(w http.ResponseWriter, r *http.Request) {
		s.writeOrderDetail(w, r, id)
	})(w, r)
}

type orderCustomerBody struct {
	Name  optional[string] `json:"name"`
	Email optional[string] `json:"email"`
	Phone optional[string] `json:"phone"`
}

type orderLineBody struct {
	VariantID optional[uuid.UUID] `json:"variant_id"`
	Qty       optional[int]       `json:"qty"`
	Discount  optional[int64]     `json:"discount"`
}

type orderCreateBody struct {
	Source          optional[string]               `json:"source"`
	Customer        optional[orderCustomerBody]    `json:"customer"`
	ShippingAddress optional[ShippingAddressWrite] `json:"shipping_address"`
	Lines           optional[[]orderLineBody]      `json:"lines"`
	Shipping        optional[int64]                `json:"shipping"`
	Note            optional[string]               `json:"note"`
}

// CreateOrder is manual entry (P1-105, §5.4): prices come from the catalog, a
// unit_price anywhere in the body is an unknown field, and the customer and
// address are snapshotted verbatim (BR-046, BR-076, BR-078).
func (s *Server) CreateOrder(w http.ResponseWriter, r *http.Request) {
	requirePermission(auth.PermOrdersWrite, func(w http.ResponseWriter, r *http.Request) {
		var body orderCreateBody
		// shipping_option arrives with P1-218; until then it is refused like
		// status: a known concept this endpoint does not accept yet.
		if !decodeJSON(w, r, &body, "status", "shipping_option") {
			return
		}
		if err := rejectNull(map[string]bool{"source": body.Source.Null, "customer": body.Customer.Null,
			"shipping_address": body.ShippingAddress.Null, "lines": body.Lines.Null,
			"shipping": body.Shipping.Null, "note": body.Note.Null}); err != nil {
			writeError(w, r, err)
			return
		}
		if body.Source.Value != "manual" {
			writeError(w, r, fieldErr("source", "source must be manual; storefront orders come from checkout"))
			return
		}
		if !body.Customer.Set || body.Customer.Value.Name.Value == "" {
			writeError(w, r, fieldErr("customer.name", "The customer needs at least a name."))
			return
		}
		if !body.ShippingAddress.Set {
			writeError(w, r, fieldErr("shipping_address", "A shipping address is required."))
			return
		}
		if len(body.Lines.Value) == 0 {
			writeError(w, r, fieldErr("lines", "An order needs at least one line."))
			return
		}
		if body.Shipping.Value < 0 {
			writeError(w, r, fieldErr("shipping", "shipping cannot be negative."))
			return
		}

		in := orders.CreateInput{Shipping: body.Shipping.Value}
		cust := map[string]any{"name": body.Customer.Value.Name.Value, "email": nil, "phone": nil}
		if c := body.Customer.Value; c.Email.Set && !c.Email.Null {
			cust["email"] = c.Email.Value
		}
		if c := body.Customer.Value; c.Phone.Set && !c.Phone.Null {
			cust["phone"] = c.Phone.Value
		}
		var err error
		if in.Customer, err = json.Marshal(cust); err != nil {
			writeError(w, r, err)
			return
		}
		if in.ShippingAddress, err = json.Marshal(body.ShippingAddress.Value); err != nil {
			writeError(w, r, err)
			return
		}
		if body.Note.Set && body.Note.Value != "" {
			in.Note = &body.Note.Value
		}
		for i, l := range body.Lines.Value {
			if l.VariantID.Value == uuid.Nil {
				writeError(w, r, fieldErr(fmt.Sprintf("lines.%d.variant_id", i), "variant_id is required."))
				return
			}
			if l.Qty.Value < 1 {
				writeError(w, r, fieldErr(fmt.Sprintf("lines.%d.qty", i), "qty is at least 1."))
				return
			}
			if l.Discount.Value < 0 {
				writeError(w, r, fieldErr(fmt.Sprintf("lines.%d.discount", i), "discount cannot be negative."))
				return
			}
			in.Lines = append(in.Lines, orders.CreateLine{VariantID: l.VariantID.Value,
				Qty: l.Qty.Value, Discount: l.Discount.Value})
		}

		id, err := s.orders.Create(r.Context(), in)
		if err != nil {
			writeError(w, r, err)
			return
		}
		d, err := s.orders.Get(r.Context(), id)
		if err != nil {
			writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusCreated, orderOut(d))
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

type orderExportBody struct {
	Status     optional[[]OrderStatus] `json:"status"`
	Source     optional[OrderSource]   `json:"source"`
	CustomerID optional[uuid.UUID]     `json:"customer_id"`
	RefundOwed optional[bool]          `json:"refund_owed"`
	PlacedFrom optional[string]        `json:"placed_from"`
	PlacedTo   optional[string]        `json:"placed_to"`
	Q          optional[string]        `json:"q"`
}

// ExportOrders is the accounting export (P1-107, §5.6): the §5.1 filters in
// the body, answered 202 with the job to poll (BR-060, BR-065).
func (s *Server) ExportOrders(w http.ResponseWriter, r *http.Request) {
	requirePermission(auth.PermExportsRead, func(w http.ResponseWriter, r *http.Request) {
		var body orderExportBody
		if r.ContentLength != 0 {
			if !decodeJSON(w, r, &body) {
				return
			}
		}
		if err := rejectNull(map[string]bool{"status": body.Status.Null, "source": body.Source.Null,
			"customer_id": body.CustomerID.Null, "refund_owed": body.RefundOwed.Null,
			"placed_from": body.PlacedFrom.Null, "placed_to": body.PlacedTo.Null, "q": body.Q.Null}); err != nil {
			writeError(w, r, err)
			return
		}
		var f orders.Filter
		for _, st := range body.Status.Value {
			if !st.Valid() {
				writeError(w, r, fieldErr("status", "status is pending, paid, processing, shipped, completed or cancelled"))
				return
			}
			f.Status = append(f.Status, string(st))
		}
		if body.Source.Set {
			if !body.Source.Value.Valid() {
				writeError(w, r, fieldErr("source", "source is storefront or manual"))
				return
			}
			src := string(body.Source.Value)
			f.Source = &src
		}
		if body.CustomerID.Set {
			f.CustomerID = &body.CustomerID.Value
		}
		if body.RefundOwed.Set {
			f.RefundOwed = &body.RefundOwed.Value
		}
		if body.Q.Set {
			f.Q = &body.Q.Value
		}
		var err error
		if body.PlacedFrom.Set {
			if f.PlacedFrom, err = timeParam("placed_from", &body.PlacedFrom.Value); err != nil {
				writeError(w, r, err)
				return
			}
		}
		if body.PlacedTo.Set {
			if f.PlacedTo, err = timeParam("placed_to", &body.PlacedTo.Value); err != nil {
				writeError(w, r, err)
				return
			}
		}
		id, err := s.orders.StartExport(r.Context(), f)
		if err != nil {
			writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusAccepted, JobAccepted{JobId: id})
	})(w, r)
}

func orderRowOut(o orders.Row) OrderListRow {
	return OrderListRow{Id: o.ID, OrderNumber: o.OrderNumber, Source: OrderSource(o.Source),
		Status: OrderStatus(o.Status), Version: o.Version,
		Customer: jsonbOut[OrderCustomerSnapshot](o.Customer), ItemCount: o.ItemCount,
		Total: o.Total, PlacedAt: o.PlacedAt, PaidAt: o.PaidAt, RefundedAt: o.RefundedAt}
}
