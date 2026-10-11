package orders

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/db/sqlcgen"
)

// CreateLine is one requested line; the price is never in it (BR-078).
type CreateLine struct {
	VariantID uuid.UUID
	Qty       int
	Discount  int64
}

// CreateInput is a manual order (04-api-spec.md §5.4). Customer and
// ShippingAddress are the marshalled snapshots, written verbatim (BR-076).
type CreateInput struct {
	Customer        []byte
	ShippingAddress []byte
	Lines           []CreateLine
	Shipping        int64
	Note            *string
	// ShippingOption, when set, is quoted again for Destination and charged
	// instead of Shipping (BR-121).
	ShippingOption *ShippingChoice
	Destination    string
}

// Create enters a manual order: pending, bank_transfer, numbered from the
// tenant sequence (BR-077), every line priced by variant_price() and
// snapshotted (BR-046, BR-076, BR-078).
func (s *Service) Create(ctx context.Context, in CreateInput) (uuid.UUID, error) {
	var id uuid.UUID
	err := s.tx(ctx, func(q *sqlcgen.Queries, tx pgx.Tx) error {
		lines := make([]newLine, len(in.Lines))
		parcel := Parcel{Destination: in.Destination}
		for i, l := range in.Lines {
			v, err := q.VariantForOrder(ctx, l.VariantID)
			if errors.Is(err, pgx.ErrNoRows) {
				return fieldError(lineField(i, "variant_id"), "No such variant.")
			}
			if err != nil {
				return err
			}
			if v.ArchivedAt != nil {
				return fieldError(lineField(i, "variant_id"), "This variant is archived and cannot be ordered (BR-045).")
			}
			if l.Discount > v.Price*int64(l.Qty) {
				return fieldError(lineField(i, "discount"), "A discount cannot exceed the line's total.")
			}
			lines[i] = newLine{VariantID: l.VariantID, Qty: l.Qty, Discount: l.Discount,
				SKU: deref(v.Sku), Title: titleSnapshot(v.Title, v.OptionValues), UnitPrice: v.Price}
			parcel.WeightGrams += int64(v.WeightGrams) * int64(l.Qty)
			parcel.Value += v.Price * int64(l.Qty)
		}
		order := newOrder{Source: "manual", PaymentMethod: "bank_transfer",
			Customer: in.Customer, ShippingAddress: in.ShippingAddress, Note: in.Note,
			Shipping: in.Shipping, Lines: lines}
		if in.ShippingOption != nil {
			opts, err := s.quote(ctx, q, parcel)
			if err != nil {
				return err
			}
			opt, err := chosen(opts, *in.ShippingOption)
			if err != nil {
				return err
			}
			order.Shipping, order.ShippingCourier, order.ShippingService = opt.Price, &opt.CourierCode, &opt.ServiceCode
		}
		o, err := insert(ctx, q, order)
		if err != nil {
			return err
		}
		id = o.ID
		return auditCreate(ctx, tx, o)
	})
	if err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

// newLine is an order line whose price has already been decided, through
// variant_price() and nowhere else (BR-046).
type newLine struct {
	VariantID  uuid.UUID
	Qty        int
	Discount   int64
	SKU, Title string
	UnitPrice  int64
}

// newOrder is what insert writes. Manual entry and checkout differ in where
// the lines come from, not in how an order is numbered and stored.
type newOrder struct {
	Source, PaymentMethod            string
	CustomerID, CartID               *uuid.UUID
	Customer, ShippingAddress        []byte
	Note                             *string
	Shipping                         int64
	ShippingCourier, ShippingService *string
	Lines                            []newLine
}

// insert numbers an order from the tenant sequence (BR-077) and writes it and
// its snapshotted lines (BR-076) in the caller's transaction. subtotal is
// before discounts, so the total CHECK holds by construction (BR-078). The
// caller writes the audit row.
func insert(ctx context.Context, q *sqlcgen.Queries, in newOrder) (sqlcgen.Order, error) {
	var subtotal, discount int64
	for _, l := range in.Lines {
		subtotal += l.UnitPrice * int64(l.Qty)
		discount += l.Discount
	}
	prefix, err := q.TenantOrderPrefix(ctx)
	if err != nil {
		return sqlcgen.Order{}, err
	}
	n, err := q.NextOrderNumber(ctx)
	if err != nil {
		return sqlcgen.Order{}, err
	}
	o, err := q.CreateOrder(ctx, sqlcgen.CreateOrderParams{
		ID: uuid.Must(uuid.NewV7()), Source: in.Source, CustomerID: in.CustomerID, CartID: in.CartID,
		OrderNumber: fmt.Sprintf("%s-%06d", prefix, n), Customer: in.Customer,
		ShippingAddress: in.ShippingAddress, Note: in.Note, PaymentMethod: in.PaymentMethod,
		ShippingCourier: in.ShippingCourier, ShippingService: in.ShippingService,
		Subtotal: subtotal, Shipping: in.Shipping, Discount: discount, Total: subtotal + in.Shipping - discount,
	})
	if err != nil {
		return sqlcgen.Order{}, err
	}
	for _, l := range in.Lines {
		if err := q.CreateOrderLine(ctx, sqlcgen.CreateOrderLineParams{
			ID: uuid.Must(uuid.NewV7()), OrderID: o.ID, VariantID: l.VariantID,
			Sku: l.SKU, Title: l.Title, Qty: int32(l.Qty), UnitPrice: l.UnitPrice, Discount: l.Discount,
		}); err != nil {
			return sqlcgen.Order{}, err
		}
	}
	return o, nil
}

// auditCreate is the one order.create row an order gets (BR-018), whoever
// placed it; a shopper's checkout records no actor.
func auditCreate(ctx context.Context, tx pgx.Tx, o sqlcgen.Order) error {
	return db.Audit(ctx, tx, db.AuditEntry{
		Action: "order.create", SubjectType: "order", SubjectID: o.ID.String(),
		After: map[string]any{"order_number": o.OrderNumber, "source": o.Source, "total": o.TotalAmount},
	})
}

// lineField names a field of one request line: lines.2.variant_id.
func lineField(i int, name string) string { return fmt.Sprintf("lines.%d.%s", i, name) }

// titleSnapshot is the ERD's 'Erigo Basic Tee — Black / M' (BR-076).
func titleSnapshot(title string, optionValues []string) string {
	if len(optionValues) == 0 {
		return title
	}
	return title + " — " + strings.Join(optionValues, " / ")
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
