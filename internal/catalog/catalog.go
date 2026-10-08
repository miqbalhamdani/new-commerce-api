// Package catalog is brands, categories, products, variants and media
// (Phase 1, 04-api-spec.md §6–§8). Every write runs in one InTenantTx with its
// audit row (BR-018); every read is narrowed to the caller's tenant by RLS.
package catalog

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/db/sqlcgen"
	"github.com/miqbalhamdani/new-commerce-api/internal/jobs"
	apperrors "github.com/miqbalhamdani/new-commerce-api/internal/platform/errors"
	"github.com/miqbalhamdani/new-commerce-api/internal/storage"
)

// Service is the catalog use cases.
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

// notFound turns "no row" into the 404 every catalog route answers for a row
// that is missing or belongs to another tenant -- the two are the same to a
// client (BR-011).
func notFound(err error, what string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return apperrors.NotFound("No such " + what + ".")
	}
	return err
}

func fieldError(field, detail string) error {
	return apperrors.ValidationFailed(detail).WithFields(apperrors.Field{Name: field, Detail: detail})
}

// likePattern escapes LIKE metacharacters so a search for "50%" means it.
func likePattern(q *string) *string {
	if q == nil || strings.TrimSpace(*q) == "" {
		return nil
	}
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(strings.TrimSpace(*q))
	return &r
}
