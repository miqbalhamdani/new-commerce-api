package blog

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/db/sqlcgen"
	"github.com/miqbalhamdani/new-commerce-api/internal/storage"
)

// Limits on a post (04-api-spec.md §13.2).
const (
	maxTags       = 10
	maxTagRunes   = 40
	maxProducts   = 12
	maxCoverBytes = 20 << 20
	presignTTL    = 10 * time.Minute
)

var coverTypes = map[string]string{"image/jpeg": "jpg", "image/png": "png", "image/webp": "webp"}

type Post = sqlcgen.BlogPost

// PostView is a post as the admin shows it: its references by name, its
// cover's URL, its linked products, and the excerpt the storefront will show
// (BR-132).
type PostView struct {
	Post
	CategoryName   *string
	AuthorName     *string
	CoverURL       *string
	ExcerptPreview string
	Products       []PostProduct
}

type PostProduct struct {
	ID       uuid.UUID
	Title    string
	CoverURL *string
}

// PostInput is a create or PATCH body. A nil field is omitted: the default on
// create, unchanged on PATCH. Clear* is an explicit null (BR-009).
type PostInput struct {
	Title, Slug, Body *string
	Excerpt           *string
	ClearExcerpt      bool
	CategoryID        *uuid.UUID
	ClearCategory     bool
	AuthorID          *uuid.UUID
	ClearAuthor       bool
	Tags              *[]string
	Status            *string
	PublishedAt       *time.Time
	ClearPublishedAt  bool
	IsFeatured        *bool
	CoverKey          *string
	ClearCover        bool
	ProductIDs        *[]uuid.UUID
}

// PostListRow is one row of the admin list.
type PostListRow struct {
	sqlcgen.ListBlogPostsRow
	CoverURL *string
}

// PostCursor is the keyset position after the last post of a page.
type PostCursor struct {
	CreatedAt time.Time `json:"t"`
	ID        uuid.UUID `json:"i"`
}

// PostFilter is GET /v1/blog/posts.
type PostFilter struct {
	Q          *string
	Status     *string
	CategoryID *uuid.UUID
	Archived   bool
	After      *PostCursor
	Limit      int
}

// Presign is a signed direct upload of a cover.
type Presign struct {
	URL       string
	Key       string
	ExpiresIn int
}

// CreatePost makes a post, a draft unless in says otherwise. Its slug comes
// from the title unless sent (BR-131); its author is the caller unless sent
// (BR-133). A cover needs the post's id, so it is set by a later PATCH.
func (s *Service) CreatePost(ctx context.Context, in PostInput) (PostView, error) {
	if in.CoverKey != nil {
		return PostView{}, fieldError("cover_r2_key", "set the cover once the post exists; its key carries the post's id")
	}
	id := uuid.Must(uuid.NewV7())
	p := sqlcgen.UpdateBlogPostParams{ID: id, Tags: []string{}, Status: "draft", AuthorID: actorOf(ctx)}
	if in.Title == nil {
		return PostView{}, fieldError("title", "title is required")
	}
	err := s.tx(ctx, func(q *sqlcgen.Queries, tx pgx.Tx) error {
		if err := apply(ctx, q, &p, in); err != nil {
			return err
		}
		slug, err := derivedSlug(ctx, q, p.Title, "title", in.Slug)
		if err != nil {
			return err
		}
		if p.IsFeatured {
			if err := q.ClearBlogFeatured(ctx, id); err != nil {
				return err
			}
		}
		post, err := q.CreateBlogPost(ctx, sqlcgen.CreateBlogPostParams{ID: id, CategoryID: p.CategoryID,
			AuthorID: p.AuthorID, Title: p.Title, Slug: slug, Excerpt: p.Excerpt, Body: p.Body, Tags: p.Tags,
			Status: p.Status, IsFeatured: p.IsFeatured, PublishedAt: p.PublishedAt})
		if err != nil {
			return slugTaken(err, "blog_posts_tenant_id_slug_key", in.Slug != nil, "title", "blog post")
		}
		if err := setProducts(ctx, q, id, in.ProductIDs); err != nil {
			return err
		}
		return db.Audit(ctx, tx, db.AuditEntry{Action: "blog_post.create", SubjectType: "blog_post",
			SubjectID: id.String(), After: postAudit(post, in.ProductIDs)})
	})
	if err != nil {
		return PostView{}, err
	}
	return s.GetPost(ctx, id)
}

// UpdatePost applies a PATCH. Publishing without a date sets it to now; a
// later date schedules the post; featuring it takes the flag from the post
// that had it (BR-131). Last save wins (BR-010).
func (s *Service) UpdatePost(ctx context.Context, id uuid.UUID, in PostInput) (PostView, error) {
	if in.CoverKey != nil {
		if err := s.checkCover(ctx, id, *in.CoverKey); err != nil {
			return PostView{}, err
		}
	}
	err := s.tx(ctx, func(q *sqlcgen.Queries, tx pgx.Tx) error {
		before, err := q.GetBlogPost(ctx, id)
		if err != nil {
			return notFound(err, "blog post")
		}
		if before.ArchivedAt != nil {
			return fieldError("status", "an archived post cannot be edited")
		}
		p := sqlcgen.UpdateBlogPostParams{ID: id, CategoryID: before.CategoryID, AuthorID: before.AuthorID,
			Title: before.Title, Slug: before.Slug, Excerpt: before.Excerpt, Body: before.Body,
			CoverR2Key: before.CoverR2Key, Tags: before.Tags, Status: before.Status,
			IsFeatured: before.IsFeatured, PublishedAt: before.PublishedAt}
		if err := apply(ctx, q, &p, in); err != nil {
			return err
		}
		if in.Slug != nil {
			if p.Slug, err = derivedSlug(ctx, q, p.Title, "title", in.Slug); err != nil {
				return err
			}
		}
		if in.CoverKey != nil {
			p.CoverR2Key = in.CoverKey
		} else if in.ClearCover {
			p.CoverR2Key = nil
		}
		if p.IsFeatured && !before.IsFeatured {
			if err := q.ClearBlogFeatured(ctx, id); err != nil {
				return err
			}
		}
		after, err := q.UpdateBlogPost(ctx, p)
		if err != nil {
			return slugTaken(err, "blog_posts_tenant_id_slug_key", true, "title", "blog post")
		}
		if err := setProducts(ctx, q, id, in.ProductIDs); err != nil {
			return err
		}
		return db.Audit(ctx, tx, db.AuditEntry{Action: "blog_post.update", SubjectType: "blog_post",
			SubjectID: id.String(), Before: postAudit(before, nil), After: postAudit(after, in.ProductIDs)})
	})
	if err != nil {
		return PostView{}, err
	}
	return s.GetPost(ctx, id)
}

// ArchivePost is the soft delete; it clears the featured flag (BR-012, BR-131).
func (s *Service) ArchivePost(ctx context.Context, id uuid.UUID) error {
	return s.tx(ctx, func(q *sqlcgen.Queries, tx pgx.Tx) error {
		p, err := q.ArchiveBlogPost(ctx, id)
		if err != nil {
			return notFound(err, "blog post")
		}
		return db.Audit(ctx, tx, db.AuditEntry{Action: "blog_post.archive", SubjectType: "blog_post",
			SubjectID: id.String(), After: map[string]any{"archived_at": p.ArchivedAt}})
	})
}

func (s *Service) GetPost(ctx context.Context, id uuid.UUID) (PostView, error) {
	var v PostView
	err := s.tx(ctx, func(q *sqlcgen.Queries, _ pgx.Tx) error {
		p, err := q.GetBlogPost(ctx, id)
		if err != nil {
			return notFound(err, "blog post")
		}
		names, err := q.BlogPostRefNames(ctx, id)
		if err != nil {
			return err
		}
		products, err := q.ListBlogPostProducts(ctx, id)
		if err != nil {
			return err
		}
		v = PostView{Post: p, CategoryName: names.CategoryName, AuthorName: names.AuthorName,
			CoverURL: s.publicURL(p.CoverR2Key), ExcerptPreview: ResolveExcerpt(p.Excerpt, p.Body),
			Products: make([]PostProduct, 0, len(products))}
		for _, pr := range products {
			v.Products = append(v.Products, PostProduct{ID: pr.ID, Title: pr.Title, CoverURL: s.publicURL(&pr.CoverKey)})
		}
		return nil
	})
	return v, err
}

// ListPosts returns up to f.Limit posts after the cursor, newest created
// first, and the cursor for the next page (nil on the last one).
func (s *Service) ListPosts(ctx context.Context, f PostFilter) ([]PostListRow, *PostCursor, error) {
	params := sqlcgen.ListBlogPostsParams{Archived: f.Archived, Q: likePattern(f.Q), Status: f.Status,
		CategoryID: f.CategoryID, Lim: int32(f.Limit + 1)}
	if f.After != nil {
		params.AfterCreated, params.AfterID = &f.After.CreatedAt, &f.After.ID
	}
	var rows []sqlcgen.ListBlogPostsRow
	err := s.tx(ctx, func(q *sqlcgen.Queries, _ pgx.Tx) (err error) {
		rows, err = q.ListBlogPosts(ctx, params)
		return err
	})
	if err != nil {
		return nil, nil, err
	}
	var next *PostCursor
	if len(rows) > f.Limit {
		rows = rows[:f.Limit]
		last := rows[f.Limit-1]
		next = &PostCursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}
	out := make([]PostListRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, PostListRow{ListBlogPostsRow: r, CoverURL: s.publicURL(r.CoverR2Key)})
	}
	return out, next, nil
}

// PresignCover signs an upload of a post's cover to
// {tenant}/blogs/{post}/{sha256}.{ext} (BR-053, BR-135).
func (s *Service) PresignCover(ctx context.Context, postID uuid.UUID, mime string, bytes int64, sha string) (Presign, error) {
	ext, ok := coverTypes[mime]
	if !ok {
		return Presign{}, fieldError("mime_type", "images are image/jpeg, image/png or image/webp")
	}
	if bytes > maxCoverBytes {
		return Presign{}, fieldError("bytes", "an image is at most 20 MB")
	}
	err := s.tx(ctx, func(q *sqlcgen.Queries, _ pgx.Tx) error {
		p, err := q.GetBlogPost(ctx, postID)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && p.ArchivedAt != nil) {
			return fieldError("post_id", "no such live blog post")
		}
		return err
	})
	if err != nil {
		return Presign{}, err
	}
	key := fmt.Sprintf("%s/blogs/%s/%s.%s", tenantOf(ctx), postID, sha, ext)
	u, err := s.files.PresignPut(ctx, key, presignTTL)
	return Presign{URL: u, Key: key, ExpiresIn: int(presignTTL.Seconds())}, err
}

// checkCover holds a cover key to the post's own prefix and to an uploaded
// image within the size limit (BR-051, BR-135).
func (s *Service) checkCover(ctx context.Context, postID uuid.UUID, key string) error {
	if !strings.HasPrefix(key, fmt.Sprintf("%s/blogs/%s/", tenantOf(ctx), postID)) {
		return fieldError("cover_r2_key", "the key is not a cover upload for this post")
	}
	mime, size, err := s.files.Head(ctx, key)
	if errors.Is(err, storage.ErrNotFound) {
		return fieldError("cover_r2_key", "nothing was uploaded under this key")
	}
	if err != nil {
		return err
	}
	if _, ok := coverTypes[mime]; !ok {
		return fieldError("cover_r2_key", "the uploaded object is "+mime+", not an image")
	}
	if size > maxCoverBytes {
		return fieldError("cover_r2_key", "the uploaded image is over 20 MB")
	}
	return nil
}

// apply merges in onto p, checking each field it touches.
func apply(ctx context.Context, q *sqlcgen.Queries, p *sqlcgen.UpdateBlogPostParams, in PostInput) error {
	if in.Title != nil {
		if p.Title = strings.TrimSpace(*in.Title); p.Title == "" {
			return fieldError("title", "title is required")
		}
	}
	if in.Body != nil {
		p.Body = *in.Body
	}
	if in.Excerpt != nil {
		if e := strings.TrimSpace(*in.Excerpt); e != "" {
			p.Excerpt = &e
		} else {
			p.Excerpt = nil // an empty excerpt is none (BR-132)
		}
	} else if in.ClearExcerpt {
		p.Excerpt = nil
	}
	if in.CategoryID != nil {
		c, err := q.GetBlogCategory(ctx, *in.CategoryID)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && c.ArchivedAt != nil) {
			return fieldError("category_id", "no such live blog category")
		}
		if err != nil {
			return err
		}
		p.CategoryID = in.CategoryID
	} else if in.ClearCategory {
		p.CategoryID = nil
	}
	if in.AuthorID != nil {
		if _, err := q.GetUser(ctx, *in.AuthorID); errors.Is(err, pgx.ErrNoRows) {
			return fieldError("author_id", "the author is not a user of this shop")
		} else if err != nil {
			return err
		}
		p.AuthorID = in.AuthorID
	} else if in.ClearAuthor {
		p.AuthorID = nil
	}
	if in.Tags != nil {
		tags, err := cleanTags(*in.Tags)
		if err != nil {
			return err
		}
		p.Tags = tags
	}
	if in.Status != nil {
		if *in.Status != "draft" && *in.Status != "published" {
			return fieldError("status", "status is draft or published")
		}
		p.Status = *in.Status
	}
	if in.PublishedAt != nil {
		p.PublishedAt = in.PublishedAt
	} else if in.ClearPublishedAt {
		if p.Status == "published" {
			return fieldError("published_at", "a published post has a date; move it back to draft first")
		}
		p.PublishedAt = nil
	}
	if p.Status == "published" && p.PublishedAt == nil {
		now := time.Now()
		p.PublishedAt = &now // publishing without a date publishes now (BR-131)
	}
	if in.IsFeatured != nil {
		p.IsFeatured = *in.IsFeatured
	}
	if p.Tags == nil {
		p.Tags = []string{} // tags is NOT NULL; a nil slice would be NULL
	}
	if in.ProductIDs != nil {
		if err := checkProducts(ctx, q, *in.ProductIDs); err != nil {
			return err
		}
	}
	return nil
}

// cleanTags trims each tag and drops repeats; an empty or over-long tag, or
// too many, is 422 (04-api-spec.md §13.2).
func cleanTags(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, t := range in {
		t = strings.TrimSpace(t)
		if t == "" || utf8.RuneCountInString(t) > maxTagRunes {
			return nil, fieldError("tags", fmt.Sprintf("each tag is 1 to %d characters", maxTagRunes))
		}
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	if len(out) > maxTags {
		return nil, fieldError("tags", fmt.Sprintf("a post has at most %d tags", maxTags))
	}
	return out, nil
}

// checkProducts holds the linked products to this shop's, with no repeats and
// at most 12 (BR-134).
func checkProducts(ctx context.Context, q *sqlcgen.Queries, ids []uuid.UUID) error {
	if len(ids) > maxProducts {
		return fieldError("product_ids", fmt.Sprintf("a post links at most %d products", maxProducts))
	}
	seen := map[uuid.UUID]bool{}
	for _, id := range ids {
		if seen[id] {
			return fieldError("product_ids", "a product is listed twice")
		}
		seen[id] = true
	}
	n, err := q.CountProducts(ctx, ids)
	if err != nil {
		return err
	}
	if int(n) != len(ids) {
		return fieldError("product_ids", "not every id is a product of this shop")
	}
	return nil
}

// setProducts replaces a post's linked products, in the order given; nil
// leaves them as they are.
func setProducts(ctx context.Context, q *sqlcgen.Queries, postID uuid.UUID, ids *[]uuid.UUID) error {
	if ids == nil {
		return nil
	}
	if err := q.DeleteBlogPostProducts(ctx, postID); err != nil {
		return err
	}
	for i, id := range *ids {
		if err := q.AddBlogPostProduct(ctx, sqlcgen.AddBlogPostProductParams{PostID: postID, ProductID: id,
			Position: int32(i)}); err != nil {
			return err
		}
	}
	return nil
}

func postAudit(p Post, products *[]uuid.UUID) map[string]any {
	a := map[string]any{"title": p.Title, "slug": p.Slug, "status": p.Status, "published_at": p.PublishedAt,
		"is_featured": p.IsFeatured, "category_id": p.CategoryID, "author_id": p.AuthorID,
		"cover_r2_key": p.CoverR2Key, "tags": p.Tags}
	if products != nil {
		a["product_ids"] = *products
	}
	return a
}
