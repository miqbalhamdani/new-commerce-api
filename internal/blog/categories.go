package blog

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/db/sqlcgen"
)

type Category = sqlcgen.BlogCategory

// CategoryPatch is a PATCH body: nil leaves a field as it is.
type CategoryPatch struct {
	Name     *string
	Slug     *string
	Position *int32
}

// CreateCategory uses slug, or derives it from the name when slug is nil
// (BR-130). A taken slug is 422 on slug when sent, else on name.
func (s *Service) CreateCategory(ctx context.Context, name string, slug *string, position int32) (Category, error) {
	var c Category
	err := s.tx(ctx, func(q *sqlcgen.Queries, tx pgx.Tx) error {
		name = strings.TrimSpace(name)
		if name == "" {
			return fieldError("name", "name is required")
		}
		if _, err := derivedSlug(ctx, q, name, "name", slug); err != nil {
			return err
		}
		var err error
		c, err = q.CreateBlogCategory(ctx, sqlcgen.CreateBlogCategoryParams{ID: uuid.Must(uuid.NewV7()),
			Name: name, Slug: slug, Position: position})
		if err != nil {
			return slugTaken(err, "blog_categories_tenant_id_slug_key", slug != nil, "name", "blog category")
		}
		return db.Audit(ctx, tx, db.AuditEntry{Action: "blog_category.create", SubjectType: "blog_category",
			SubjectID: c.ID.String(), After: categoryAudit(c)})
	})
	return c, err
}

// UpdateCategory renames, re-slugs or moves a category. A rename without a
// slug re-derives it, as for brands (BR-030, BR-130). Last save wins (BR-010).
func (s *Service) UpdateCategory(ctx context.Context, id uuid.UUID, p CategoryPatch) (Category, error) {
	var c Category
	err := s.tx(ctx, func(q *sqlcgen.Queries, tx pgx.Tx) error {
		before, err := q.GetBlogCategory(ctx, id)
		if err != nil {
			return notFound(err, "blog category")
		}
		params := sqlcgen.UpdateBlogCategoryParams{ID: id, Name: before.Name, Slug: p.Slug,
			Rename: p.Name != nil, Position: before.Position}
		if p.Name != nil {
			if params.Name = strings.TrimSpace(*p.Name); params.Name == "" {
				return fieldError("name", "name is required")
			}
		}
		if p.Position != nil {
			params.Position = *p.Position
		}
		if p.Name != nil || p.Slug != nil {
			if _, err := derivedSlug(ctx, q, params.Name, "name", p.Slug); err != nil {
				return err
			}
		}
		if c, err = q.UpdateBlogCategory(ctx, params); err != nil {
			return slugTaken(err, "blog_categories_tenant_id_slug_key", p.Slug != nil, "name", "blog category")
		}
		return db.Audit(ctx, tx, db.AuditEntry{Action: "blog_category.update", SubjectType: "blog_category",
			SubjectID: id.String(), Before: categoryAudit(before), After: categoryAudit(c)})
	})
	return c, err
}

// ArchiveCategory is the soft delete (BR-012). Its posts keep it; the
// storefront shows them with no category (BR-130).
func (s *Service) ArchiveCategory(ctx context.Context, id uuid.UUID) error {
	return s.tx(ctx, func(q *sqlcgen.Queries, tx pgx.Tx) error {
		c, err := q.ArchiveBlogCategory(ctx, id)
		if err != nil {
			return notFound(err, "blog category")
		}
		return db.Audit(ctx, tx, db.AuditEntry{Action: "blog_category.archive", SubjectType: "blog_category",
			SubjectID: id.String(), After: map[string]any{"archived_at": c.ArchivedAt}})
	})
}

// ListCategories is every category, live or archived, by position then name.
func (s *Service) ListCategories(ctx context.Context, archived bool) ([]Category, error) {
	var rows []Category
	err := s.tx(ctx, func(q *sqlcgen.Queries, _ pgx.Tx) (err error) {
		rows, err = q.ListBlogCategories(ctx, archived)
		return err
	})
	return rows, err
}

func categoryAudit(c Category) map[string]any {
	return map[string]any{"name": c.Name, "slug": c.Slug, "position": c.Position}
}
