// Package orders is the order state machine, list, detail, manual entry,
// customers and export (Phase 2, 04-api-spec.md §5). Every write runs in one
// InTenantTx with its audit row (BR-018); status changes only through
// Transition (BR-071).
package orders

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/db/sqlcgen"
	"github.com/miqbalhamdani/new-commerce-api/internal/jobs"
	apperrors "github.com/miqbalhamdani/new-commerce-api/internal/platform/errors"
	"github.com/miqbalhamdani/new-commerce-api/internal/storage"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

// Service is the order use cases.
type Service struct {
	store *db.Store
	files *storage.Store
	jobs  *jobs.Service
}

func NewService(store *db.Store, files *storage.Store, j *jobs.Service) *Service {
	return &Service{store: store, files: files, jobs: j}
}

// tx runs fn in the caller's tenant with a query set bound to the transaction.
func (s *Service) tx(ctx context.Context, fn func(q *sqlcgen.Queries, tx pgx.Tx) error) error {
	return s.store.InTenantTx(ctx, func(tx pgx.Tx) error { return fn(sqlcgen.New(tx), tx) })
}

// notFound turns "no row" into the 404 every order route answers for a row
// that is missing or belongs to another tenant -- the two are the same to a
// client (BR-011). Deliberately duplicated from catalog: domain packages do
// not import each other.
func notFound(err error, what string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return apperrors.NotFound("No such " + what + ".")
	}
	return err
}

func fieldError(field, detail string) error {
	return apperrors.ValidationFailed(detail).WithFields(apperrors.Field{Name: field, Detail: detail})
}

// tenantOf and actorOf read what the middleware or the job runner put on the
// context.
func tenantOf(ctx context.Context) uuid.UUID {
	id, _ := tenant.FromContext(ctx)
	return id
}

func actorOf(ctx context.Context) *uuid.UUID {
	if a, ok := tenant.ActorFromContext(ctx); ok {
		return &a.UserID
	}
	return nil
}

// likePattern escapes LIKE metacharacters so a search for "50%" means it.
func likePattern(q *string) *string {
	if q == nil || strings.TrimSpace(*q) == "" {
		return nil
	}
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(strings.TrimSpace(*q))
	return &r
}
