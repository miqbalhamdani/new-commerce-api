// Package blog is blog categories and posts (M6, 04-api-spec.md §13 and
// §11.11). Every write runs in one InTenantTx with its audit row (BR-018);
// every read is narrowed to the caller's tenant by RLS.
package blog

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/db/sqlcgen"
	apperrors "github.com/miqbalhamdani/new-commerce-api/internal/platform/errors"
	"github.com/miqbalhamdani/new-commerce-api/internal/storage"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

// Service is the blog use cases.
type Service struct {
	store *db.Store
	files *storage.Store
}

func NewService(store *db.Store, files *storage.Store) *Service {
	return &Service{store: store, files: files}
}

// slugPattern is a URL slug: lower-case a-z0-9 joined by single hyphens.
var slugPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// tx runs fn in the caller's tenant with a query set bound to the transaction.
func (s *Service) tx(ctx context.Context, fn func(q *sqlcgen.Queries, tx pgx.Tx) error) error {
	return s.store.InTenantTx(ctx, func(tx pgx.Tx) error { return fn(sqlcgen.New(tx), tx) })
}

// notFound turns "no row" into a 404; another tenant's row is the same as a
// missing one (BR-011).
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

// publicURL turns a stored object key into a URL on the image domain (BR-050).
func (s *Service) publicURL(key *string) *string {
	if key == nil || *key == "" {
		return nil
	}
	u := s.files.PublicURL(*key)
	return &u
}

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

// derivedSlug checks a slug the client sent, or derives one from text, which
// must then slugify to something; field names what a bad derived one blames.
func derivedSlug(ctx context.Context, q *sqlcgen.Queries, text, field string, slug *string) (string, error) {
	if slug != nil {
		if !slugPattern.MatchString(*slug) {
			return "", fieldError("slug", "slug is lower-case letters and digits joined by single hyphens")
		}
		return *slug, nil
	}
	derived, err := q.Slugify(ctx, text)
	if err != nil {
		return "", err
	}
	if derived == "" {
		return "", fieldError(field, field+" needs at least one letter or digit")
	}
	return derived, nil
}

// slugTaken is the unique violation on (tenant_id, slug) as a 422: on slug
// when the client sent one, else on the field it was derived from.
func slugTaken(err error, constraint string, sent bool, field, what string) error {
	if code, c, ok := db.Violation(err); ok && code == "23505" && c == constraint {
		if sent {
			return fieldError("slug", "another "+what+" already uses this slug (deleted ones included)")
		}
		return fieldError(field, "another "+what+" already has this "+field+
			", or one that slugifies the same (deleted ones included); set a different slug")
	}
	return err
}
