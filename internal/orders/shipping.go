package orders

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/biteship"
	"github.com/miqbalhamdani/new-commerce-api/internal/db/sqlcgen"
	apperrors "github.com/miqbalhamdani/new-commerce-api/internal/platform/errors"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

// Shipping quotes (BR-120, BR-121) for manual orders and checkout.

// ShippingChoice is a courier service picked from a quote.
type ShippingChoice struct {
	Courier, Service string
}

// Parcel is what a quote is for: the destination, the total weight and the
// value of the goods, in minor units.
type Parcel struct {
	Destination string
	WeightGrams int64
	Value       int64
}

// quote is the shop's options for a parcel, read through q: the caller may be
// holding a row lock with the one connection it has, so nothing here takes
// another from the pool. Biteship failing is the 502 (BR-121).
func (s *Service) quote(ctx context.Context, q *sqlcgen.Queries, p Parcel) ([]biteship.Option, error) {
	r := biteship.Request{Destination: p.Destination, WeightGrams: p.WeightGrams, Value: p.Value}
	st, err := q.GetStorefrontSettings(ctx)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if err == nil && st.OriginPostalCode != nil {
		r.Origin, r.Couriers = *st.OriginPostalCode, st.ShippingCouriers
	}
	if s.Rates == nil {
		return []biteship.Option{}, nil
	}
	tenantID, _ := tenant.FromContext(ctx)
	opts, err := s.Rates.Quote(ctx, tenantID, r)
	if errors.Is(err, biteship.ErrUnavailable) {
		return nil, apperrors.ShippingRatesUnavailable(err)
	}
	return opts, err
}

// chosen is the option the shopper picked, from a fresh quote: a service
// no longer offered is 409 shipping_unavailable (BR-121).
func chosen(opts []biteship.Option, c ShippingChoice) (biteship.Option, error) {
	for _, o := range opts {
		if o.CourierCode == c.Courier && o.ServiceCode == c.Service {
			return o, nil
		}
	}
	return biteship.Option{}, apperrors.ShippingUnavailable()
}

// ShippingRates is the admin quote for a manual order's lines (§5.4): the
// same weights and prices the order would be made from.
func (s *Service) ShippingRates(ctx context.Context, destination string, lines []CreateLine) (int64, []biteship.Option, error) {
	var (
		weight int64
		opts   []biteship.Option
	)
	err := s.tx(ctx, func(q *sqlcgen.Queries, _ pgx.Tx) error {
		p := Parcel{Destination: destination}
		for i, l := range lines {
			v, err := q.VariantForOrder(ctx, l.VariantID)
			if errors.Is(err, pgx.ErrNoRows) || err == nil && v.ArchivedAt != nil {
				return fieldError(lineField(i, "variant_id"), "No such variant, or it is archived.")
			}
			if err != nil {
				return err
			}
			p.WeightGrams += int64(v.WeightGrams) * int64(l.Qty)
			p.Value += v.Price * int64(l.Qty)
		}
		weight = p.WeightGrams
		var err error
		opts, err = s.quote(ctx, q, p)
		return err
	})
	return weight, opts, err
}

// CartRates is the storefront quote for a cart's available items (§11.6),
// the same parcel checkout charges for. An unknown, expired or checked-out
// cart is 404; one with nothing available to ship is empty_cart.
func (s *Service) CartRates(ctx context.Context, cartID uuid.UUID, destination string) (int64, []biteship.Option, error) {
	var (
		weight int64
		opts   []biteship.Option
	)
	err := s.tx(ctx, func(q *sqlcgen.Queries, _ pgx.Tx) error {
		cart, err := q.GetCart(ctx, cartID)
		if errors.Is(err, pgx.ErrNoRows) || err == nil && (cart.Status != "open" || !cart.ExpiresAt.After(time.Now())) {
			return apperrors.NotFound("No such cart.")
		}
		if err != nil {
			return err
		}
		items, err := q.CartLines(ctx, cartID)
		if err != nil {
			return err
		}
		p := cartParcel(items, destination)
		if p.WeightGrams == 0 && p.Value == 0 {
			return apperrors.EmptyCart()
		}
		weight = p.WeightGrams
		opts, err = s.quote(ctx, q, p)
		return err
	})
	return weight, opts, err
}

// cartParcel is a cart's available items as one parcel: unavailable ones are
// neither charged nor shipped (BR-090).
func cartParcel(items []sqlcgen.CartLinesRow, destination string) Parcel {
	p := Parcel{Destination: destination}
	for _, it := range items {
		if it.Available == nil || !*it.Available || it.PriceAmount == nil || it.WeightGrams == nil {
			continue
		}
		p.WeightGrams += int64(*it.WeightGrams) * int64(it.Qty)
		p.Value += *it.PriceAmount * int64(it.Qty)
	}
	return p
}
