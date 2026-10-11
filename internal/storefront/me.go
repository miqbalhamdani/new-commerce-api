package storefront

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/miqbalhamdani/new-commerce-api/internal/db/sqlcgen"
	"github.com/miqbalhamdani/new-commerce-api/internal/httpkit"
	"github.com/miqbalhamdani/new-commerce-api/internal/orders"
	apperrors "github.com/miqbalhamdani/new-commerce-api/internal/platform/errors"
)

// Me, order history and the guest lookup: 04-api-spec.md §11.9 (P1-208).
// Personal data, so never with a key alone (BR-082).

func (s *Server) GetMe(w http.ResponseWriter, r *http.Request) {
	requireCustomer(func(w http.ResponseWriter, r *http.Request, customerID uuid.UUID) {
		c, err := s.customers.Me(r.Context(), customerID)
		if err != nil {
			httpkit.WriteError(w, r, err)
			return
		}
		httpkit.WriteJSON(w, http.StatusOK, Me{Id: c.ID, Email: openapi_types.Email(c.Email), Name: c.Name,
			Phone: c.Phone, CreatedAt: c.CreatedAt})
	})(w, r)
}

func (s *Server) UpdateMe(w http.ResponseWriter, r *http.Request) {
	requireCustomer(func(w http.ResponseWriter, r *http.Request, customerID uuid.UUID) {
		var body struct {
			Name  httpkit.Optional[string] `json:"name"`
			Phone httpkit.Optional[string] `json:"phone"`
		}
		// email is not editable in v2 (§11.9): refused by name rather than
		// as an unknown field, so the reason is in the answer.
		if !httpkit.DecodeJSON(w, r, &body, "email") {
			return
		}
		if err := httpkit.RejectNull(map[string]bool{"name": body.Name.Null}); err != nil {
			httpkit.WriteError(w, r, err)
			return
		}
		var name, phone *string
		if body.Name.Set {
			name = &body.Name.Value
		}
		if body.Phone.Set && !body.Phone.Null {
			phone = &body.Phone.Value
		}
		c, err := s.customers.UpdateMe(r.Context(), customerID, name, body.Phone.Set, phone)
		if err != nil {
			httpkit.WriteError(w, r, err)
			return
		}
		httpkit.WriteJSON(w, http.StatusOK, Me{Id: c.ID, Email: openapi_types.Email(c.Email), Name: c.Name,
			Phone: c.Phone, CreatedAt: c.CreatedAt})
	})(w, r)
}

func (s *Server) ListMyOrders(w http.ResponseWriter, r *http.Request, params ListMyOrdersParams) {
	requireCustomer(func(w http.ResponseWriter, r *http.Request, customerID uuid.UUID) {
		var after *orders.HistoryCursor
		if params.Cursor != nil {
			after = &orders.HistoryCursor{}
			if err := httpkit.DecodeCursor(params.Cursor, after); err != nil {
				httpkit.WriteError(w, r, err)
				return
			}
		}
		rows, next, err := s.orders.CustomerOrders(r.Context(), customerID, after, httpkit.PageLimit(params.Limit))
		if err != nil {
			httpkit.WriteError(w, r, err)
			return
		}
		page := MyOrderPage{Data: make([]MyOrderRow, 0, len(rows))}
		for _, o := range rows {
			page.Data = append(page.Data, MyOrderRow{OrderNumber: o.OrderNumber, Status: OrderStatus(o.Status),
				Total: o.TotalAmount, ItemCount: int(o.ItemCount), PlacedAt: o.PlacedAt})
		}
		if next != nil {
			page.NextCursor = httpkit.EncodeCursor(next)
		}
		httpkit.WriteJSON(w, http.StatusOK, page)
	})(w, r)
}

func (s *Server) GetMyOrder(w http.ResponseWriter, r *http.Request, orderNumber OrderNumber) {
	requireCustomer(func(w http.ResponseWriter, r *http.Request, customerID uuid.UUID) {
		o, lines, err := s.orders.ByNumber(r.Context(), orderNumber)
		// Another customer's order is the same 404 as none at all (BR-011).
		if errors.Is(err, orders.ErrNoSuchOrder) || err == nil && (o.CustomerID == nil || *o.CustomerID != customerID) {
			httpkit.WriteError(w, r, apperrors.NotFound("No such order."))
			return
		}
		if err != nil {
			httpkit.WriteError(w, r, err)
			return
		}
		httpkit.WriteJSON(w, http.StatusOK, customerOrderOut(o, lines))
	})(w, r)
}

// GetGuestOrder opens one order with the token its checkout returned
// (BR-091). Every failure, an unknown order number included, is the same 401:
// order numbers are sequential, and a 404 would let anyone list them.
func (s *Server) GetGuestOrder(w http.ResponseWriter, r *http.Request, orderNumber OrderNumber) {
	token := r.Header.Get("X-Order-Token")
	o, lines, err := s.orders.ByNumber(r.Context(), orderNumber)
	if err != nil && !errors.Is(err, orders.ErrNoSuchOrder) {
		httpkit.WriteError(w, r, err)
		return
	}
	if err != nil || token == "" || !s.orderTokens.Valid(o.TenantID, o.ID, token) {
		httpkit.WriteError(w, r, apperrors.CustomerAuthRequired("Send this order's X-Order-Token, from its checkout."))
		return
	}
	httpkit.WriteJSON(w, http.StatusOK, customerOrderOut(o, lines))
}

func customerOrderOut(o sqlcgen.Order, lines []sqlcgen.OrderLine) CustomerOrder {
	out := CustomerOrder{OrderNumber: o.OrderNumber, Status: OrderStatus(o.Status), Lines: make([]OrderLine, 0, len(lines)),
		Subtotal: o.SubtotalAmount, Shipping: o.ShippingAmount, Discount: o.DiscountAmount, Total: o.TotalAmount,
		PaymentMethod: CustomerOrderPaymentMethod(o.PaymentMethod), Courier: o.Courier, TrackingNumber: o.TrackingNumber,
		PlacedAt: o.PlacedAt, PaidAt: o.PaidAt, ShippedAt: o.ShippedAt, CompletedAt: o.CompletedAt, CancelledAt: o.CancelledAt}
	for _, l := range lines {
		line := OrderLine{Title: l.TitleSnapshot, Qty: int(l.Qty), UnitPrice: l.UnitPrice}
		if l.SkuSnapshot != "" {
			line.Sku = &l.SkuSnapshot
		}
		out.Lines = append(out.Lines, line)
	}
	_ = json.Unmarshal(o.ShippingAddress, &out.ShippingAddress)
	return out
}
