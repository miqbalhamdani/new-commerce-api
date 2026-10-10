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
}

// Create enters a manual order: pending, bank_transfer, numbered from the
// tenant sequence (BR-077), every line priced by variant_price() and
// snapshotted (BR-046, BR-076, BR-078).
func (s *Service) Create(ctx context.Context, in CreateInput) (uuid.UUID, error) {
	id := uuid.Must(uuid.NewV7())
	err := s.tx(ctx, func(q *sqlcgen.Queries, tx pgx.Tx) error {
		type snapshot struct {
			sku, title string
			unitPrice  int64
		}
		var subtotal, discount int64
		snaps := make([]snapshot, len(in.Lines))
		for i, l := range in.Lines {
			field := func(name string) string { return fmt.Sprintf("lines.%d.%s", i, name) }
			v, err := q.VariantForOrder(ctx, l.VariantID)
			if errors.Is(err, pgx.ErrNoRows) {
				return fieldError(field("variant_id"), "No such variant.")
			}
			if err != nil {
				return err
			}
			if v.ArchivedAt != nil {
				return fieldError(field("variant_id"), "This variant is archived and cannot be ordered (BR-045).")
			}
			lineTotal := v.Price * int64(l.Qty)
			if l.Discount > lineTotal {
				return fieldError(field("discount"), "A discount cannot exceed the line's total.")
			}
			snaps[i] = snapshot{sku: deref(v.Sku), title: titleSnapshot(v.Title, v.OptionValues), unitPrice: v.Price}
			subtotal += lineTotal
			discount += l.Discount
		}

		prefix, err := q.TenantOrderPrefix(ctx)
		if err != nil {
			return err
		}
		n, err := q.NextOrderNumber(ctx)
		if err != nil {
			return err
		}
		number := fmt.Sprintf("%s-%06d", prefix, n)

		o, err := q.CreateOrder(ctx, sqlcgen.CreateOrderParams{
			ID: id, OrderNumber: number, Customer: in.Customer, ShippingAddress: in.ShippingAddress,
			Note: in.Note, Subtotal: subtotal, Shipping: in.Shipping, Discount: discount,
			Total: subtotal + in.Shipping - discount,
		})
		if err != nil {
			return err
		}
		for i, l := range in.Lines {
			if err := q.CreateOrderLine(ctx, sqlcgen.CreateOrderLineParams{
				ID: uuid.Must(uuid.NewV7()), OrderID: id, VariantID: l.VariantID,
				Sku: snaps[i].sku, Title: snaps[i].title,
				Qty: int32(l.Qty), UnitPrice: snaps[i].unitPrice, Discount: l.Discount,
			}); err != nil {
				return err
			}
		}
		return db.Audit(ctx, tx, db.AuditEntry{
			Action: "order.create", SubjectType: "order", SubjectID: id.String(),
			After: map[string]any{"order_number": o.OrderNumber, "source": "manual", "total": o.TotalAmount},
		})
	})
	if err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

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
