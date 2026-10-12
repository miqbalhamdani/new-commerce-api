package httpapi

import (
	"fmt"
	"net/http"
	"regexp"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/biteship"
	"github.com/miqbalhamdani/new-commerce-api/internal/orders"
)

// Shipping rates for a manual order: 04-api-spec.md §5.4 (P1-218).

var postalCode = regexp.MustCompile(`^[0-9]{5}$`)

func (s *Server) ShippingRates(w http.ResponseWriter, r *http.Request) {
	requirePermission(auth.PermOrdersWrite, func(w http.ResponseWriter, r *http.Request) {
		var body ShippingRatesRequest
		if !decodeJSON(w, r, &body) {
			return
		}
		if !postalCode.MatchString(body.DestinationPostalCode) {
			writeError(w, r, fieldErr("destination_postal_code", "destination_postal_code is 5 digits"))
			return
		}
		if len(body.Lines) == 0 {
			writeError(w, r, fieldErr("lines", "A quote needs at least one line."))
			return
		}
		lines := make([]orders.CreateLine, 0, len(body.Lines))
		for i, l := range body.Lines {
			if l.Qty < 1 {
				writeError(w, r, fieldErr(fmt.Sprintf("lines.%d.qty", i), "qty is at least 1."))
				return
			}
			lines = append(lines, orders.CreateLine{VariantID: l.VariantId, Qty: l.Qty})
		}
		weight, opts, err := s.orders.ShippingRates(r.Context(), body.DestinationPostalCode, lines)
		if err != nil {
			writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, ShippingQuote{WeightGrams: int(weight), Options: shippingOptionsOut(opts)})
	})(w, r)
}

func shippingOptionsOut(opts []biteship.Option) []ShippingOption {
	out := make([]ShippingOption, 0, len(opts))
	for _, o := range opts {
		out = append(out, ShippingOption{CourierCode: o.CourierCode, CourierServiceCode: o.ServiceCode,
			CourierName: o.CourierName, ServiceName: o.ServiceName, Price: o.Price, Duration: o.Duration})
	}
	return out
}
