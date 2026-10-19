package blog

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/db/sqlcgen"
	apperrors "github.com/miqbalhamdani/new-commerce-api/internal/platform/errors"
)

// The storefront's blog reads (P1-605, 04-api-spec.md §11.11). Posts come
// only from storefront_blog_posts, which encodes "live" (BR-081, BR-131).

// relatedPosts is how many related posts a post page shows.
const relatedPosts = 3

// ShopPost is a live post as a shop's website shows it.
type ShopPost struct {
	ID                         uuid.UUID
	Slug, Title, Excerpt, Body string
	CoverURL                   *string
	CategorySlug, CategoryName *string
	AuthorName, AuthorBio      *string
	Tags                       []string
	Featured                   bool
	PublishedAt                time.Time
}

// ShopFilter is GET /v1/storefront/blog/posts.
type ShopFilter struct {
	Category *string
	Tag      *string
	Featured *bool
	After    *ShopCursor
	Page     int // 1-based; 0 means keyset paging with After
	Limit    int
}

// ShopCursor is the keyset position after a page, newest first.
type ShopCursor struct {
	At time.Time `json:"t"`
	ID uuid.UUID `json:"i"`
}

type ShopPage struct {
	Posts []ShopPost
	Next  *ShopCursor
	Total int64
}

func (s *Service) ShopCategories(ctx context.Context) ([]sqlcgen.StorefrontBlogCategoriesRow, error) {
	var rows []sqlcgen.StorefrontBlogCategoriesRow
	err := s.tx(ctx, func(q *sqlcgen.Queries, _ pgx.Tx) (err error) {
		rows, err = q.StorefrontBlogCategories(ctx)
		return err
	})
	return rows, err
}

// ShopPosts is the live posts, newest first; f.Page pages by offset for a
// numbered pager instead of by keyset, and the total is counted either way.
func (s *Service) ShopPosts(ctx context.Context, f ShopFilter) (ShopPage, error) {
	params := sqlcgen.StorefrontBlogPostsParams{Category: f.Category, Tag: f.Tag, Featured: f.Featured,
		Lim: int32(f.Limit + 1)}
	if f.Page > 0 {
		params.Off = int32((f.Page - 1) * f.Limit)
	} else if f.After != nil {
		params.AfterPublished, params.AfterID = &f.After.At, &f.After.ID
	}
	var page ShopPage
	err := s.tx(ctx, func(q *sqlcgen.Queries, _ pgx.Tx) error {
		rows, err := q.StorefrontBlogPosts(ctx, params)
		if err != nil {
			return err
		}
		if page.Total, err = q.CountStorefrontBlogPosts(ctx, sqlcgen.CountStorefrontBlogPostsParams{
			Category: f.Category, Tag: f.Tag, Featured: f.Featured}); err != nil {
			return err
		}
		if len(rows) > f.Limit {
			rows = rows[:f.Limit]
			last := rows[f.Limit-1]
			page.Next = &ShopCursor{At: *last.PublishedAt, ID: last.ID}
		}
		page.Posts = s.shopPosts(rows)
		return nil
	})
	return page, err
}

// ShopPost is one live post by slug, its linked product ids in order (the
// caller loads the visible ones, BR-134) and its related posts.
func (s *Service) ShopPost(ctx context.Context, slug string) (ShopPost, []uuid.UUID, []ShopPost, error) {
	var (
		post    ShopPost
		ids     []uuid.UUID
		related []ShopPost
	)
	err := s.tx(ctx, func(q *sqlcgen.Queries, _ pgx.Tx) error {
		rows, err := q.StorefrontBlogPosts(ctx, sqlcgen.StorefrontBlogPostsParams{Slug: &slug, Lim: 1})
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return apperrors.NotFound("No such blog post.")
		}
		post = s.shopPosts(rows)[0]
		if ids, err = q.BlogPostProductIDs(ctx, post.ID); err != nil {
			return err
		}
		// From the post's category, or from every post when it has none --
		// an archived category counts as none (BR-130).
		cat := rows[0].CategoryID
		if rows[0].CategorySlug == nil {
			cat = nil
		}
		rel, err := q.StorefrontBlogPosts(ctx, sqlcgen.StorefrontBlogPostsParams{CategoryID: cat,
			NotID: &post.ID, Lim: relatedPosts})
		related = s.shopPosts(rel)
		return err
	})
	return post, ids, related, err
}

func (s *Service) shopPosts(rows []sqlcgen.StorefrontBlogPostsRow) []ShopPost {
	out := make([]ShopPost, 0, len(rows))
	for _, r := range rows {
		p := ShopPost{ID: r.ID, Slug: r.Slug, Title: r.Title, Excerpt: ResolveExcerpt(r.Excerpt, r.Body),
			Body: r.Body, CoverURL: s.publicURL(r.CoverR2Key), CategorySlug: r.CategorySlug,
			CategoryName: r.CategoryName, AuthorName: r.AuthorName, AuthorBio: r.AuthorBio, Tags: r.Tags,
			Featured: r.IsFeatured}
		if r.PublishedAt != nil {
			p.PublishedAt = *r.PublishedAt
		}
		if p.Tags == nil {
			p.Tags = []string{}
		}
		out = append(out, p)
	}
	return out
}
