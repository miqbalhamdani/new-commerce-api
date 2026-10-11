package storefront

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/miqbalhamdani/new-commerce-api/internal/db/sqlcgen"
	"github.com/miqbalhamdani/new-commerce-api/internal/httpkit"
	"github.com/miqbalhamdani/new-commerce-api/internal/orders"
	apperrors "github.com/miqbalhamdani/new-commerce-api/internal/platform/errors"
)

// Checkout: 04-api-spec.md §11.7 (P1-205), bank transfer only until P1-219
// and P1-220 bring shipping and Midtrans.

var postalCode = regexp.MustCompile(`^[0-9]{5}$`)

func (s *Server) Checkout(w http.ResponseWriter, r *http.Request, cartID CartId) {
	var body CheckoutRequest
	if !httpkit.DecodeJSON(w, r, &body) {
		return
	}
	// A signed-in customer's contact defaults to their profile, and the order
	// goes into their history (BR-095); the customer comes from the token,
	// never from the cart.
	customerID := signedIn(r)
	if body.Contact == nil && customerID != nil {
		me, err := s.customers.Me(r.Context(), *customerID)
		if err != nil {
			httpkit.WriteError(w, r, err)
			return
		}
		body.Contact = &Contact{Name: me.Name, Email: openapi_types.Email(me.Email), Phone: me.Phone}
	}
	// Checked before the cart is locked: a bad request should not queue
	// behind someone else's checkout to be told so.
	if err := validCheckout(body); err != nil {
		httpkit.WriteError(w, r, err)
		return
	}
	customer, _ := json.Marshal(map[string]any{"name": strings.TrimSpace(body.Contact.Name),
		"email": strings.ToLower(string(body.Contact.Email)), "phone": body.Contact.Phone})
	address, _ := json.Marshal(map[string]any{"line1": body.ShippingAddress.Line1, "line2": body.ShippingAddress.Line2,
		"city": body.ShippingAddress.City, "province": body.ShippingAddress.Province,
		"postal_code": body.ShippingAddress.PostalCode})

	o, lines, created, err := s.orders.Checkout(r.Context(), orders.CheckoutInput{
		CartID: cartID, CustomerID: customerID, Customer: customer, ShippingAddress: address,
		PaymentMethod: string(body.PaymentMethod), Note: body.Note,
		Shipping:    orders.ShippingChoice{Courier: body.ShippingOption.CourierCode, Service: body.ShippingOption.CourierServiceCode},
		Destination: body.ShippingAddress.PostalCode,
	})
	if err != nil {
		httpkit.WriteError(w, r, err)
		return
	}
	status := http.StatusOK // a replay: the order this cart already made (BR-088)
	if created {
		status = http.StatusCreated
	}
	httpkit.WriteJSON(w, status, s.checkoutOut(o, lines))
}

func validCheckout(b CheckoutRequest) error {
	var bad []apperrors.Field
	need := func(field, value string) {
		if strings.TrimSpace(value) == "" {
			bad = append(bad, apperrors.Field{Name: field, Detail: field + " is required"})
		}
	}
	if b.Contact == nil {
		b.Contact = &Contact{}
	}
	need("contact.name", b.Contact.Name)
	need("contact.email", string(b.Contact.Email))
	need("shipping_address.line1", b.ShippingAddress.Line1)
	need("shipping_address.city", b.ShippingAddress.City)
	need("shipping_address.province", b.ShippingAddress.Province)
	if !postalCode.MatchString(b.ShippingAddress.PostalCode) {
		bad = append(bad, apperrors.Field{Name: "shipping_address.postal_code", Detail: "postal_code is 5 digits"})
	}
	if b.ShippingOption.CourierCode == "" || b.ShippingOption.CourierServiceCode == "" {
		bad = append(bad, apperrors.Field{Name: "shipping_option", Detail: "shipping_option is required: a courier service from the shipping rates"})
	}
	if b.PaymentMethod == "" {
		bad = append(bad, apperrors.Field{Name: "payment_method", Detail: "payment_method is required"})
	}
	if len(bad) > 0 {
		return apperrors.ValidationFailed("Some checkout details are missing or invalid.").WithFields(bad...)
	}
	return nil
}

func (s *Server) checkoutOut(o sqlcgen.Order, lines []sqlcgen.OrderLine) CheckoutOrder {
	out := CheckoutOrder{OrderNumber: o.OrderNumber, Status: o.Status, Lines: make([]OrderLine, 0, len(lines)),
		Subtotal: o.SubtotalAmount, Shipping: o.ShippingAmount, Total: o.TotalAmount,
		ShippingCourier: o.ShippingCourier, ShippingService: o.ShippingService,
		PaymentMethod: CheckoutOrderPaymentMethod(o.PaymentMethod), PlacedAt: o.PlacedAt}
	for _, l := range lines {
		line := OrderLine{Title: l.TitleSnapshot, Qty: int(l.Qty), UnitPrice: l.UnitPrice}
		if l.SkuSnapshot != "" {
			line.Sku = &l.SkuSnapshot
		}
		out.Lines = append(out.Lines, line)
	}
	// A guest's proof the order is theirs; a signed-in customer has their
	// account instead (BR-091).
	if o.CustomerID == nil {
		tok := s.orderTokens.Mint(o.TenantID, o.ID)
		out.OrderToken = &tok
	}
	return out
}
