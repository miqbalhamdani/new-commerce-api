package orders

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/db/sqlcgen"
	apperrors "github.com/miqbalhamdani/new-commerce-api/internal/platform/errors"
)

// UpdateInput is the §5.2 PATCH: only the address, the note and the shipping
// amount, and only while pending (BR-079).
type UpdateInput struct {
	ShippingAddress []byte // the new snapshot, marshalled; nil leaves it alone
	Note            *string
	ClearNote       bool
	Shipping        *int64
}

// UpdatePending edits a pending order at the version in If-Match (BR-010); the
// total is recomputed from the new shipping amount in the same UPDATE.
func (s *Service) UpdatePending(ctx context.Context, id uuid.UUID, version int, in UpdateInput) (sqlcgen.Order, error) {
	var out sqlcgen.Order
	err := s.tx(ctx, func(q *sqlcgen.Queries, tx pgx.Tx) error {
		o, err := q.LockOrder(ctx, id)
		if err != nil {
			return notFound(err, "order")
		}
		if o.Status != "pending" {
			return apperrors.ValidationFailed("Only a pending order can be edited (BR-079).")
		}
		if int(o.Version) != version {
			return apperrors.VersionConflict(int(o.Version), version)
		}

		params := sqlcgen.UpdatePendingOrderParams{ID: id, ShippingAddress: in.ShippingAddress, Shipping: in.Shipping}
		if in.ClearNote || in.Note != nil {
			params.SetNote = true
			params.Note = in.Note
		}
		if out, err = q.UpdatePendingOrder(ctx, params); err != nil {
			return err
		}

		before, after := map[string]any{}, map[string]any{}
		if in.ShippingAddress != nil {
			before["shipping_address"] = json.RawMessage(o.ShippingAddress)
			after["shipping_address"] = json.RawMessage(in.ShippingAddress)
		}
		if params.SetNote {
			before["note"] = o.Note
			after["note"] = in.Note
		}
		if in.Shipping != nil {
			before["shipping"] = o.ShippingAmount
			after["shipping"] = *in.Shipping
		}
		return db.Audit(ctx, tx, db.AuditEntry{
			Action: "order.update", SubjectType: "order", SubjectID: id.String(),
			Before: before, After: after,
		})
	})
	return out, err
}
