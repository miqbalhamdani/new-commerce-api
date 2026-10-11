package storefront

import (
	"net/http"
	"regexp"

	"github.com/miqbalhamdani/new-commerce-api/internal/biteship"
	"github.com/miqbalhamdani/new-commerce-api/internal/httpkit"
)

// Shipping rates for a cart: 04-api-spec.md §11.6 (P1-218).

var postalCodeRe = regexp.MustCompile(`^[0-9]{5}$`)

func (s *Server) CartShippingRates(w http.ResponseWriter, r *http.Request, cartID CartId) {
	var body ShippingRatesRequest
	if !httpkit.DecodeJSON(w, r, &body) {
		return
	}
	if !postalCodeRe.MatchString(body.DestinationPostalCode) {
		httpkit.WriteError(w, r, fieldError("destination_postal_code", "destination_postal_code is 5 digits"))
		return
	}
	weight, opts, err := s.orders.CartRates(r.Context(), cartID, body.DestinationPostalCode)
	if err != nil {
		httpkit.WriteError(w, r, err)
		return
	}
	httpkit.WriteJSON(w, http.StatusOK, ShippingQuote{WeightGrams: int(weight), Options: shippingOptionsOut(opts)})
}

func shippingOptionsOut(opts []biteship.Option) []ShippingOption {
	out := make([]ShippingOption, 0, len(opts))
	for _, o := range opts {
		out = append(out, ShippingOption{CourierCode: o.CourierCode, CourierServiceCode: o.ServiceCode,
			CourierName: o.CourierName, ServiceName: o.ServiceName, Price: o.Price, Duration: o.Duration})
	}
	return out
}
