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
}

// Checkout turns a cart into an order, once (BR-088). It returns the order,
// its lines, and whether this call made it: false is a replay.
//
// The cart's row lock is what makes it once. Twenty taps of "pay" queue on
// the lock; the first makes the order and marks the cart checked out; each of
// the others, unblocked, reads the cart again, finds it checked out, and
// returns that order. orders.cart_id is UNIQUE behind it in case anything
// ever bypasses the lock.
func (s *Service) Checkout(ctx context.Context, in CheckoutInput) (sqlcgen.Order, []sqlcgen.OrderLine, bool, error) {
	// ponytail: no storefront_settings until P1-216, so every shop takes bank
	// transfer and nothing else (BR-122's default).
	if in.PaymentMethod != "bank_transfer" {
		return sqlcgen.Order{}, nil, false, fieldError("payment_method", "This shop does not take "+in.PaymentMethod+".")
	}
	var (
		o       sqlcgen.Order
		lines   []sqlcgen.OrderLine
		created bool
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
			if o, err = q.OrderByCart(ctx, &in.CartID); err != nil {
				return err
			}
			lines, err = q.GetOrderLines(ctx, o.ID)
			return err
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

		o, err = insert(ctx, q, newOrder{Source: "storefront", PaymentMethod: in.PaymentMethod,
			CustomerID: in.CustomerID, CartID: &in.CartID, Customer: in.Customer,
			ShippingAddress: in.ShippingAddress, Note: in.Note, Lines: newLines})
		if err != nil {
			return err
		}
		if err := q.CheckOutCart(ctx, in.CartID); err != nil {
			return err
		}
		if err := auditCreate(ctx, tx, o); err != nil {
			return err
		}
		created = true
		lines, err = q.GetOrderLines(ctx, o.ID)
		return err
	})
	if err != nil {
		return sqlcgen.Order{}, nil, false, err
	}
	return o, lines, created, nil
}
