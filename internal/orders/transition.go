package orders

import (
	"context"
	"regexp"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/db/sqlcgen"
	apperrors "github.com/miqbalhamdani/new-commerce-api/internal/platform/errors"
)

// allowed is BR-070 verbatim. completed and cancelled move nowhere.
var allowed = map[string][]string{
	"pending":    {"paid", "cancelled"},
	"paid":       {"processing", "cancelled"},
	"processing": {"shipped", "cancelled"},
	"shipped":    {"completed"},
	"completed":  {},
	"cancelled":  {},
}

// AllowedFrom is the order detail's allowed_transitions: the client renders
// actions from it and never hardcodes the list (04-api-spec.md §5.2).
func AllowedFrom(status string) []string {
	return allowed[status]
}

// courierRe mirrors the orders.courier CHECK so a bad code is a 422 naming the
// field, not a constraint violation surfacing as a 500.
var courierRe = regexp.MustCompile(`^[a-z0-9_]+$`)

// TransitionInput carries the optional bodies of the status routes: courier
// and tracking number on ship (BR-072), a reason on cancel, which has no
// column and lands in the audit row only (BR-073).
type TransitionInput struct {
	Courier        *string
	TrackingNumber *string
	Reason         *string
}

// Transition is the one function that changes an order's status (BR-071). It
// locks the row, treats a move to the current status as a successful no-op
// (no version bump, no audit), refuses anything off the allow-list, stamps the
// matching timestamp, bumps version and writes exactly one audit row.
func (s *Service) Transition(ctx context.Context, id uuid.UUID, to string, in TransitionInput) (sqlcgen.Order, error) {
	var out sqlcgen.Order
	err := s.tx(ctx, func(q *sqlcgen.Queries, tx pgx.Tx) error {
		o, err := q.LockOrder(ctx, id)
		if err != nil {
			return notFound(err, "order")
		}
		if o.Status == to {
			// BR-071: a repeated click or callback is harmless.
			out = o
			return nil
		}
		if !slices.Contains(allowed[o.Status], to) {
			return apperrors.IllegalTransition(o.Status, to)
		}

		params := sqlcgen.TransitionOrderParams{ID: id, ToStatus: to}
		if to == "shipped" {
			// BR-072: tracking is required; courier defaults to what the
			// shopper chose, so a manual order with none must send it.
			if in.TrackingNumber == nil || strings.TrimSpace(*in.TrackingNumber) == "" {
				return fieldError("tracking_number", "Shipping needs a tracking number.")
			}
			courier := in.Courier
			if courier == nil || *courier == "" {
				courier = o.ShippingCourier
			}
			if courier == nil || *courier == "" {
				return fieldError("courier", "This order has no chosen courier; send one.")
			}
			if !courierRe.MatchString(*courier) {
				return fieldError("courier", "A courier is a lower-case Biteship code, like jne.")
			}
			params.Courier = courier
			params.TrackingNumber = in.TrackingNumber
		}

		out, err = q.TransitionOrder(ctx, params)
		if err != nil {
			return err
		}

		after := map[string]any{"status": to}
		if to == "shipped" {
			after["courier"] = *params.Courier
			after["tracking_number"] = *params.TrackingNumber
		}
		if in.Reason != nil && strings.TrimSpace(*in.Reason) != "" {
			after["reason"] = *in.Reason
		}
		return db.Audit(ctx, tx, db.AuditEntry{
			Action: "order.transition", SubjectType: "order", SubjectID: id.String(),
			Before: map[string]any{"status": o.Status}, After: after,
		})
	})
	return out, err
}
