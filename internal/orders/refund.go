package orders

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/db/sqlcgen"
	apperrors "github.com/miqbalhamdani/new-commerce-api/internal/platform/errors"
)

// Refund records a refund made outside the system (BR-075): refunded_at is
// set once, on a cancelled order that was paid; the status does not change.
// The row lock serialises it with transitions.
func (s *Service) Refund(ctx context.Context, id uuid.UUID, note *string) (sqlcgen.Order, error) {
	var out sqlcgen.Order
	err := s.tx(ctx, func(q *sqlcgen.Queries, tx pgx.Tx) error {
		o, err := q.LockOrder(ctx, id)
		if err != nil {
			return notFound(err, "order")
		}
		if o.RefundedAt != nil {
			return apperrors.ValidationFailed("This order's refund is already recorded.")
		}
		if o.Status != "cancelled" || o.PaidAt == nil {
			return apperrors.ValidationFailed("A refund is recorded only on a cancelled order that was paid.")
		}
		out, err = q.RecordRefund(ctx, id)
		if err != nil {
			return err
		}
		after := map[string]any{"refunded_at": out.RefundedAt}
		if note != nil && strings.TrimSpace(*note) != "" {
			after["note"] = *note
		}
		return db.Audit(ctx, tx, db.AuditEntry{
			Action: "order.refund", SubjectType: "order", SubjectID: id.String(),
			Before: map[string]any{"refunded_at": nil}, After: after,
		})
	})
	return out, err
}
