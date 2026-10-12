package orders

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/db/sqlcgen"
	"github.com/miqbalhamdani/new-commerce-api/internal/jobs"
)

// purgeParams is a customer_purge job's params.
type purgeParams struct {
	CustomerID uuid.UUID `json:"customer_id"`
}

// StartPurge queues the erasure of one customer's personal data (P1-215,
// BR-097). The customer must exist in this shop; the job row and its audit
// entry commit together, and the message is enqueued only after (BR-060).
func (s *Service) StartPurge(ctx context.Context, customerID uuid.UUID) (uuid.UUID, error) {
	var job jobs.Job
	err := s.tx(ctx, func(q *sqlcgen.Queries, tx pgx.Tx) error {
		if _, err := q.GetCustomer(ctx, customerID); err != nil {
			return notFound(err, "customer")
		}
		var err error
		if job, err = jobs.Create(ctx, tx, "customer_purge", purgeParams{customerID}, actorOf(ctx)); err != nil {
			return err
		}
		return db.Audit(ctx, tx, db.AuditEntry{Action: "customer.purge_request", SubjectType: "job",
			SubjectID: job.ID.String(), After: purgeParams{customerID}})
	})
	if err != nil {
		return uuid.Nil, err
	}
	return job.ID, s.jobs.Enqueue(ctx, job)
}

// PurgeHandler is the worker half: everything BR-097 lists, in one
// transaction, so a purge is all or nothing. Running it twice changes nothing
// the first run did not.
func (s *Service) PurgeHandler() jobs.Handler {
	return func(ctx context.Context, job jobs.Job, _ jobs.Progress) (any, error) {
		var p purgeParams
		if err := json.Unmarshal(job.Params, &p); err != nil {
			return nil, err
		}
		var orderIDs []uuid.UUID
		err := s.tx(ctx, func(q *sqlcgen.Queries, tx pgx.Tx) error {
			email, err := q.PurgeCustomer(ctx, p.CustomerID)
			if err != nil {
				return notFound(err, "customer")
			}
			if err := q.DeleteCustomerIdentities(ctx, p.CustomerID); err != nil {
				return err
			}
			if err := q.DeleteCustomerSessions(ctx, p.CustomerID); err != nil {
				return err
			}
			orderIDs, err = q.RedactCustomerOrders(ctx, sqlcgen.RedactCustomerOrdersParams{CustomerID: &p.CustomerID, Email: email})
			if err != nil {
				return err
			}
			if err := q.DropOrderNotifications(ctx, orderIDs); err != nil {
				return err
			}
			subjects := make([]string, len(orderIDs))
			for i, id := range orderIDs {
				subjects[i] = id.String()
			}
			if err := q.RedactOrderAudit(ctx, subjects); err != nil {
				return err
			}
			return db.Audit(ctx, tx, db.AuditEntry{Action: "customer.purge", SubjectType: "customer",
				SubjectID: p.CustomerID.String(), After: map[string]int{"orders": len(orderIDs)}})
		})
		if err != nil {
			return nil, err
		}
		return map[string]int{"orders": len(orderIDs)}, nil
	}
}
