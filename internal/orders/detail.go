package orders

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/db/sqlcgen"
)

// HistoryEntry is one row of the order's audit trail, already narrowed to what
// the detail response carries (04-api-spec.md §5.2).
type HistoryEntry struct {
	ActorID   *uuid.UUID
	ActorName *string
	Action    string
	From, To  *string
	CreatedAt time.Time
}

// Detail is the §5.2 order: the row, its lines and its trail.
type Detail struct {
	Order   sqlcgen.Order
	Lines   []sqlcgen.OrderLine
	History []HistoryEntry
}

// Get reads one order with its lines and its embedded audit trail, newest
// first. The trail rides on orders:read by design -- ops work the order detail
// and have no audit_log:read (BR-073).
func (s *Service) Get(ctx context.Context, id uuid.UUID) (Detail, error) {
	var d Detail
	err := s.tx(ctx, func(q *sqlcgen.Queries, _ pgx.Tx) error {
		o, err := q.GetOrder(ctx, id)
		if err != nil {
			return notFound(err, "order")
		}
		d.Order = o
		if d.Lines, err = q.GetOrderLines(ctx, id); err != nil {
			return err
		}
		rows, err := q.OrderAuditTrail(ctx, id.String())
		if err != nil {
			return err
		}
		d.History = make([]HistoryEntry, 0, len(rows))
		for _, r := range rows {
			d.History = append(d.History, HistoryEntry{
				ActorID: r.ActorID, ActorName: r.ActorName, Action: r.Action,
				From: statusOf(r.Before), To: statusOf(r.After), CreatedAt: r.CreatedAt,
			})
		}
		return nil
	})
	return d, err
}

// statusOf pulls the status out of an audit row's before/after JSON; only
// order.transition rows carry one, the rest answer nil.
func statusOf(raw []byte) *string {
	if len(raw) == 0 {
		return nil
	}
	var m struct {
		Status *string `json:"status"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	return m.Status
}
