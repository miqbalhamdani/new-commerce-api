package orders

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/db/sqlcgen"
	apperrors "github.com/miqbalhamdani/new-commerce-api/internal/platform/errors"
)

// CheckoutInput is a storefront checkout (04-api-spec.md §11.7). Customer
// and ShippingAddress are the marshalled snapshots (BR-076). CustomerID is
// the signed-in customer, from their token and never from the cart (BR-095).
type CheckoutInput struct {
	CartID                    uuid.UUID
	CustomerID                *uuid.UUID
	Customer, ShippingAddress []byte
	PaymentMethod             string
	Note                      *string
	// Shipping is the courier service picked from /shipping-rates, quoted
	// again here for Destination (BR-121).
	Shipping    ShippingChoice
	Destination string
}

// CheckoutResult is what a checkout answers: the order and its lines,
// whether this call made it (false is a replay), and for Midtrans its live
// link -- nil, with PaymentError, when Midtrans did not give one (BR-123).
type CheckoutResult struct {
	Order        sqlcgen.Order
	Lines        []sqlcgen.OrderLine
	Created      bool
	Payment      *PayLink
	PaymentError bool
}

// Checkout turns a cart into an order, once (BR-088).
//
// The cart's row lock is what makes it once. Twenty taps of "pay" queue on
// the lock; the first makes the order and marks the cart checked out; each of
// the others, unblocked, reads the cart again, finds it checked out, and
// returns that order. orders.cart_id is UNIQUE behind it in case anything
// ever bypasses the lock.
func (s *Service) Checkout(ctx context.Context, in CheckoutInput) (CheckoutResult, error) {
	var (
		res     CheckoutResult
		st      sqlcgen.StorefrontSetting
		attempt *sqlcgen.Payment
	)
	err := s.tx(ctx, func(q *sqlcgen.Queries, tx pgx.Tx) error {
		// By id alone: a status filter would be re-checked against the
		// winner's committed row, and every waiter would find no cart.
		cart, err := q.LockCart(ctx, in.CartID)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.NotFound("No such cart.")
		}
		if err != nil {
			return err
		}
		if cart.Status == "checked_out" {
			// A fresh statement, so a fresh snapshot: it sees the order the
			// lock holder committed.
			if res.Order, err = q.OrderByCart(ctx, &in.CartID); err != nil {
				return err
			}
			if res.Lines, err = q.GetOrderLines(ctx, res.Order.ID); err != nil {
				return err
			}
			if p, err := q.LatestPayment(ctx, res.Order.ID); err == nil {
				attempt = &p
			} else if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			return nil
		}
		if !cart.ExpiresAt.After(time.Now()) {
			return apperrors.NotFound("No such cart.")
		}

		items, err := q.CartLines(ctx, in.CartID)
		if err != nil {
			return err
		}
		if len(items) == 0 {
			return apperrors.EmptyCart()
		}
		var (
			unavailable []apperrors.Field
			newLines    = make([]newLine, 0, len(items))
		)
		for _, it := range items {
			title := titleSnapshot(it.ProductTitle, it.OptionValues)
			if it.Available == nil || !*it.Available || it.PriceAmount == nil {
				unavailable = append(unavailable, apperrors.Field{Name: "items",
					Extra: map[string]any{"variant_id": it.VariantID, "sku": it.Sku, "title": title}})
				continue
			}
			newLines = append(newLines, newLine{VariantID: it.VariantID, Qty: int(it.Qty),
				SKU: deref(it.Sku), Title: title, UnitPrice: *it.PriceAmount})
		}
		if len(unavailable) > 0 {
			// The cart is left as it was: the shopper decides what to do
			// about each item (BR-090).
			return apperrors.ItemUnavailable(unavailable...)
		}

		if st, err = settingsOf(ctx, q); err != nil {
			return err
		}
		if !takes(st, in.PaymentMethod) {
			return fieldError("payment_method", "This shop does not take "+in.PaymentMethod+".")
		}

		// Quoted again, after the replay branch above so a replay never
		// waits on Biteship, and through q so the lock holder takes no second
		// connection. Within the quote's window this is the price shown
		// (BR-121); a failure here makes no order.
		opts, err := s.quote(ctx, q, cartParcel(items, in.Destination))
		if err != nil {
			return err
		}
		ship, err := chosen(opts, in.Shipping)
		if err != nil {
			return err
		}

		o, err := insert(ctx, q, newOrder{Source: "storefront", PaymentMethod: in.PaymentMethod,
			CustomerID: in.CustomerID, CartID: &in.CartID, Customer: in.Customer,
			ShippingAddress: in.ShippingAddress, Note: in.Note, Lines: newLines,
			Shipping: ship.Price, ShippingCourier: &ship.CourierCode, ShippingService: &ship.ServiceCode})
		if err != nil {
			return err
		}
		if in.PaymentMethod == "midtrans" {
			if err := wholeRupiah(o.TotalAmount); err != nil {
				return err
			}
			p, err := newAttempt(ctx, q, o, 1)
			if err != nil {
				return err
			}
			attempt = &p
		}
		if err := q.CheckOutCart(ctx, in.CartID); err != nil {
			return err
		}
		if err := auditCreate(ctx, tx, o); err != nil {
			return err
		}
		res.Order, res.Created = o, true
		res.Lines, err = q.GetOrderLines(ctx, o.ID)
		return err
	})
	if err != nil {
		return CheckoutResult{}, err
	}
	if res.Created {
		s.queueMail(ctx, "order_placed", res.Order.TenantID, res.Order.ID)
	}
	if res.Order.PaymentMethod != "midtrans" {
		return res, nil
	}
	// The order has committed; only now is Midtrans asked for a link, so a
	// slow or failing Midtrans never undoes the order (BR-123).
	switch {
	case res.Created && attempt != nil:
		res.Payment, err = s.openSnap(ctx, st, res.Order, *attempt)
		res.PaymentError = err != nil
	case live(attempt, res.Order):
		res.Payment = linkOf(attempt)
	default:
		res.PaymentError = true
	}
	return res, nil
}
