package httpapi

import (
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/blog"
)

// Blog posts: 04-api-spec.md §13.2 (P1-603).

type blogPostCreateBody struct {
	Title       optional[string]      `json:"title"`
	Slug        optional[string]      `json:"slug"`
	Body        optional[string]      `json:"body"`
	Excerpt     optional[string]      `json:"excerpt"`
	CategoryID  optional[uuid.UUID]   `json:"category_id"`
	AuthorID    optional[uuid.UUID]   `json:"author_id"`
	Tags        optional[[]string]    `json:"tags"`
	Status      optional[string]      `json:"status"`
	PublishedAt optional[time.Time]   `json:"published_at"`
	IsFeatured  optional[bool]        `json:"is_featured"`
	ProductIDs  optional[[]uuid.UUID] `json:"product_ids"`
}

// The cover is set once the post exists (BR-135), so only PATCH takes it.
type blogPostUpdateBody struct {
	blogPostCreateBody
	CoverR2Key optional[string] `json:"cover_r2_key"`
}

func (b blogPostCreateBody) input() blog.PostInput {
	var in blog.PostInput
	if b.Title.Set && !b.Title.Null {
		in.Title = &b.Title.Value
	}
	if b.Slug.Set && !b.Slug.Null {
		in.Slug = &b.Slug.Value
	}
	if b.Body.Set && !b.Body.Null {
		in.Body = &b.Body.Value
	}
	if b.Excerpt.Set && !b.Excerpt.Null {
		in.Excerpt = &b.Excerpt.Value
	}
	in.ClearExcerpt = b.Excerpt.Null
	if b.CategoryID.Set && !b.CategoryID.Null {
		in.CategoryID = &b.CategoryID.Value
	}
	in.ClearCategory = b.CategoryID.Null
	if b.AuthorID.Set && !b.AuthorID.Null {
		in.AuthorID = &b.AuthorID.Value
	}
	in.ClearAuthor = b.AuthorID.Null
	if b.Tags.Set && !b.Tags.Null {
		in.Tags = &b.Tags.Value
	}
	if b.Status.Set && !b.Status.Null {
		in.Status = &b.Status.Value
	}
	if b.PublishedAt.Set && !b.PublishedAt.Null {
		in.PublishedAt = &b.PublishedAt.Value
	}
	in.ClearPublishedAt = b.PublishedAt.Null
	if b.IsFeatured.Set && !b.IsFeatured.Null {
		in.IsFeatured = &b.IsFeatured.Value
	}
	if b.ProductIDs.Set && !b.ProductIDs.Null {
		in.ProductIDs = &b.ProductIDs.Value
	}
	return in
}

// notNull is every field that may not be null on PATCH (BR-009).
func (b blogPostCreateBody) notNull() map[string]bool {
	return map[string]bool{"title": b.Title.Null, "slug": b.Slug.Null, "body": b.Body.Null,
		"tags": b.Tags.Null, "status": b.Status.Null, "is_featured": b.IsFeatured.Null,
		"product_ids": b.ProductIDs.Null}
}

func (s *Server) ListBlogPosts(w http.ResponseWriter, r *http.Request, params ListBlogPostsParams) {
	requirePermission(auth.PermBlogRead, func(w http.ResponseWriter, r *http.Request) {
		f := blog.PostFilter{Q: params.Q, CategoryID: params.CategoryId,
			Archived: params.Archived != nil && *params.Archived, Limit: pageLimit(params.Limit)}
		if params.Status != nil {
			st := string(*params.Status)
			f.Status = &st
		}
		if params.Cursor != nil {
			f.After = &blog.PostCursor{}
			if err := decodeCursor(params.Cursor, f.After); err != nil {
				writeError(w, r, err)
				return
			}
		}
		rows, next, err := s.blog.ListPosts(r.Context(), f)
		if err != nil {
			writeError(w, r, err)
			return
		}
		page := BlogPostPage{Data: make([]BlogPostListItem, 0, len(rows))}
		for _, p := range rows {
			page.Data = append(page.Data, BlogPostListItem{Id: p.ID, Title: p.Title, Slug: p.Slug,
				Status: BlogPostStatus(p.Status), PublishedAt: p.PublishedAt, IsFeatured: p.IsFeatured,
				Category: ref(p.CategoryID, p.CategoryName), Author: ref(p.AuthorID, p.AuthorName),
				CoverUrl: p.CoverURL, ArchivedAt: p.ArchivedAt, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt})
		}
		if next != nil {
			page.NextCursor = encodeCursor(next)
		}
		writeJSON(w, http.StatusOK, page)
	})(w, r)
}

func (s *Server) CreateBlogPost(w http.ResponseWriter, r *http.Request) {
	requirePermission(auth.PermBlogWrite, func(w http.ResponseWriter, r *http.Request) {
		var body blogPostCreateBody
		if !decodeJSON(w, r, &body) {
			return
		}
		nulls := body.notNull()
		for name, isNull := range map[string]bool{"excerpt": body.Excerpt.Null, "category_id": body.CategoryID.Null,
			"author_id": body.AuthorID.Null, "published_at": body.PublishedAt.Null} {
			nulls[name] = isNull
		}
		if err := rejectNull(nulls); err != nil {
			writeError(w, r, err)
			return
		}
		v, err := s.blog.CreatePost(r.Context(), body.input())
		if err != nil {
			writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusCreated, blogPostOut(v))
	})(w, r)
}

func (s *Server) GetBlogPost(w http.ResponseWriter, r *http.Request, id Id) {
	requirePermission(auth.PermBlogRead, func(w http.ResponseWriter, r *http.Request) {
		v, err := s.blog.GetPost(r.Context(), id)
		if err != nil {
			writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, blogPostOut(v))
	})(w, r)
}

func (s *Server) UpdateBlogPost(w http.ResponseWriter, r *http.Request, id Id) {
	requirePermission(auth.PermBlogWrite, func(w http.ResponseWriter, r *http.Request) {
		var body blogPostUpdateBody
		if !decodeJSON(w, r, &body) {
			return
		}
		if err := rejectNull(body.notNull()); err != nil {
			writeError(w, r, err)
			return
		}
		in := body.input()
		if body.CoverR2Key.Set && !body.CoverR2Key.Null {
			in.CoverKey = &body.CoverR2Key.Value
		}
		in.ClearCover = body.CoverR2Key.Null
		v, err := s.blog.UpdatePost(r.Context(), id, in)
		if err != nil {
			writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, blogPostOut(v))
	})(w, r)
}

func (s *Server) ArchiveBlogPost(w http.ResponseWriter, r *http.Request, id Id) {
	requirePermission(auth.PermBlogWrite, func(w http.ResponseWriter, r *http.Request) {
		if err := s.blog.ArchivePost(r.Context(), id); err != nil {
			writeError(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})(w, r)
}

func blogPostOut(v blog.PostView) BlogPost {
	out := BlogPost{Id: v.ID, Title: v.Title, Slug: v.Slug, Status: BlogPostStatus(v.Status),
		PublishedAt: v.PublishedAt, IsFeatured: v.IsFeatured, Category: ref(v.CategoryID, v.CategoryName),
		Author: ref(v.AuthorID, v.AuthorName), Excerpt: v.Excerpt, ExcerptPreview: v.ExcerptPreview,
		Body: v.Body, Tags: v.Tags, CoverR2Key: v.CoverR2Key, CoverUrl: v.CoverURL,
		Products: make([]BlogPostProduct, 0, len(v.Products)), ArchivedAt: v.ArchivedAt,
		CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt}
	if out.Tags == nil {
		out.Tags = []string{}
	}
	for _, p := range v.Products {
		out.Products = append(out.Products, BlogPostProduct{Id: p.ID, Title: p.Title, CoverUrl: p.CoverURL})
	}
	return out
}

// ref is a reference expanded on read, or null when there is none.
func ref(id *uuid.UUID, name *string) *Ref {
	if id == nil || name == nil {
		return nil
	}
	return &Ref{Id: *id, Name: *name}
}
