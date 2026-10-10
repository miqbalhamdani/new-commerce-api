package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/orders"
)

// Order status routes (P1-102, 04-api-spec.md §5.3) and the Order DTO. Every
// route calls the one Transition (BR-071) and answers the full order; none
// takes If-Match -- the row lock and the allow-list are the concurrency
// control, and a repeat is a harmless no-op.

func (s *Server) MarkOrderPaid(w http.ResponseWriter, r *http.Request, id Id) {
	s.transitionRoute("paid", func(*http.Request) (orders.TransitionInput, bool) {
		return orders.TransitionInput{}, true
	})(w, r, id)
}

func (s *Server) ProcessOrder(w http.ResponseWriter, r *http.Request, id Id) {
	s.transitionRoute("processing", func(*http.Request) (orders.TransitionInput, bool) {
		return orders.TransitionInput{}, true
	})(w, r, id)
}

func (s *Server) CompleteOrder(w http.ResponseWriter, r *http.Request, id Id) {
	s.transitionRoute("completed", func(*http.Request) (orders.TransitionInput, bool) {
		return orders.TransitionInput{}, true
	})(w, r, id)
}

func (s *Server) ShipOrder(w http.ResponseWriter, r *http.Request, id Id) {
	s.transitionRoute("shipped", func(r *http.Request) (orders.TransitionInput, bool) {
		var body OrderShipRequest
		if !decodeJSON(w, r, &body) {
			return orders.TransitionInput{}, false
		}
		// An empty tracking_number falls through to the service's 422.
		in := orders.TransitionInput{Courier: body.Courier}
		if body.TrackingNumber != "" {
			in.TrackingNumber = &body.TrackingNumber
		}
		return in, true
	})(w, r, id)
}

func (s *Server) CancelOrder(w http.ResponseWriter, r *http.Request, id Id) {
	s.transitionRoute("cancelled", func(r *http.Request) (orders.TransitionInput, bool) {
		var in orders.TransitionInput
		if r.ContentLength != 0 {
			var body OrderCancelRequest
			if !decodeJSON(w, r, &body) {
				return in, false
			}
			in.Reason = body.Reason
		}
		return in, true
	})(w, r, id)
}

// RefundOrder is not a transition: the status stays cancelled and refunded_at
// is set once (BR-075).
func (s *Server) RefundOrder(w http.ResponseWriter, r *http.Request, id Id) {
	requirePermission(auth.PermOrdersWrite, func(w http.ResponseWriter, r *http.Request) {
		var note *string
		if r.ContentLength != 0 {
			var body OrderRefundRequest
			if !decodeJSON(w, r, &body) {
				return
			}
			note = body.Note
		}
		if _, err := s.orders.Refund(r.Context(), id, note); err != nil {
			writeError(w, r, err)
			return
		}
		s.writeOrderDetail(w, r, id)
	})(w, r)
}

// transitionRoute wraps one status route: permission, body, Transition, then
// the full order re-read so the response carries lines, history and the new
// allowed_transitions.
func (s *Server) transitionRoute(to string, input func(*http.Request) (orders.TransitionInput, bool)) func(http.ResponseWriter, *http.Request, Id) {
	return func(w http.ResponseWriter, r *http.Request, id Id) {
		requirePermission(auth.PermOrdersWrite, func(w http.ResponseWriter, r *http.Request) {
			in, ok := input(r)
			if !ok {
				return
			}
			if _, err := s.orders.Transition(r.Context(), id, to, in); err != nil {
				writeError(w, r, err)
				return
			}
			s.writeOrderDetail(w, r, id)
		})(w, r)
	}
}

func (s *Server) writeOrderDetail(w http.ResponseWriter, r *http.Request, id Id) {
	d, err := s.orders.Get(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, orderOut(d))
}

func orderOut(d orders.Detail) Order {
	o := d.Order
	out := Order{
		Id: o.ID, OrderNumber: o.OrderNumber, Source: OrderSource(o.Source),
		Status: OrderStatus(o.Status), Version: int(o.Version), CustomerId: o.CustomerID,
		Customer: jsonbOut[OrderCustomerSnapshot](o.Customer), ShippingAddress: jsonbOut[ShippingAddress](o.ShippingAddress),
		Note:     o.Note,
		Subtotal: o.SubtotalAmount, Shipping: o.ShippingAmount, Discount: o.DiscountAmount, Total: o.TotalAmount,
		PaymentMethod: OrderPaymentMethod(o.PaymentMethod), Payments: []OrderPayment{},
		ShippingCourier: o.ShippingCourier, ShippingService: o.ShippingService,
		Courier: o.Courier, TrackingNumber: o.TrackingNumber,
		PlacedAt: o.PlacedAt, PaidAt: o.PaidAt, ShippedAt: o.ShippedAt,
		CompletedAt: o.CompletedAt, CancelledAt: o.CancelledAt, RefundedAt: o.RefundedAt,
	}
	out.Lines = make([]OrderLine, len(d.Lines))
	for i, l := range d.Lines {
		out.Lines[i] = OrderLine{Id: l.ID, VariantId: l.VariantID, Sku: l.SkuSnapshot,
			Title: l.TitleSnapshot, Qty: int(l.Qty), UnitPrice: l.UnitPrice, Discount: l.DiscountAmount}
	}
	out.AllowedTransitions = make([]OrderStatus, 0, 2)
	for _, t := range orders.AllowedFrom(o.Status) {
		out.AllowedTransitions = append(out.AllowedTransitions, OrderStatus(t))
	}
	out.History = make([]OrderHistoryEntry, len(d.History))
	for i, h := range d.History {
		e := OrderHistoryEntry{Action: h.Action, CreatedAt: h.CreatedAt}
		if h.ActorID != nil {
			ref := Ref{Id: *h.ActorID}
			if h.ActorName != nil {
				ref.Name = *h.ActorName
			}
			e.Actor = &ref
		}
		if h.From != nil {
			f := OrderStatus(*h.From)
			e.From = &f
		}
		if h.To != nil {
			t := OrderStatus(*h.To)
			e.To = &t
		}
		out.History[i] = e
	}
	return out
}

// jsonbOut decodes a snapshot column; the writer controls the shape, so a
// decode failure can only mean a zero value, which renders as empty fields.
func jsonbOut[T any](raw []byte) T {
	var v T
	_ = json.Unmarshal(raw, &v)
	return v
}
