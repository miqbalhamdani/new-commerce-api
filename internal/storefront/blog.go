package storefront

import (
	"net/http"

	"github.com/google/uuid"

	"github.com/miqbalhamdani/new-commerce-api/internal/blog"
	"github.com/miqbalhamdani/new-commerce-api/internal/catalog"
	"github.com/miqbalhamdani/new-commerce-api/internal/httpkit"
)

// Blog: 04-api-spec.md §11.11 (P1-605). Key only; everything here is public
// by design. A post's products come from the catalog, so only visible ones
// show (BR-134); the two domains meet here, not in each other.

func (s *Server) ListBlogCategories(w http.ResponseWriter, r *http.Request) {
	rows, err := s.blog.ShopCategories(r.Context())
	if err != nil {
		httpkit.WriteError(w, r, err)
		return
	}
	list := BlogCategoryList{Data: make([]BlogCategoryRef, 0, len(rows))}
	for _, c := range rows {
		list.Data = append(list.Data, BlogCategoryRef{Slug: c.Slug, Name: c.Name})
	}
	httpkit.WriteJSON(w, http.StatusOK, list)
}

func (s *Server) ListBlogPosts(w http.ResponseWriter, r *http.Request, params ListBlogPostsParams) {
	f := blog.ShopFilter{Category: params.Category, Tag: params.Tag, Featured: params.Featured,
		Limit: httpkit.PageLimit(params.Limit)}
	var err error
	if f.Page, err = httpkit.PageNumber(params.Page, params.Cursor); err != nil {
		httpkit.WriteError(w, r, err)
		return
	}
	if params.Cursor != nil {
		f.After = &blog.ShopCursor{}
		if err := httpkit.DecodeCursor(params.Cursor, f.After); err != nil {
			httpkit.WriteError(w, r, err)
			return
		}
	}
	res, err := s.blog.ShopPosts(r.Context(), f)
	if err != nil {
		httpkit.WriteError(w, r, err)
		return
	}
	page := BlogPostPage{Data: blogPostItems(res.Posts), Total: int(res.Total)}
	if res.Next != nil {
		page.NextCursor = httpkit.EncodeCursor(res.Next)
	}
	httpkit.WriteJSON(w, http.StatusOK, page)
}

func (s *Server) GetBlogPost(w http.ResponseWriter, r *http.Request, slug string) {
	post, ids, related, err := s.blog.ShopPost(r.Context(), slug)
	if err != nil {
		httpkit.WriteError(w, r, err)
		return
	}
	products, err := s.postProducts(r, ids)
	if err != nil {
		httpkit.WriteError(w, r, err)
		return
	}
	item := blogPostItem(post)
	httpkit.WriteJSON(w, http.StatusOK, BlogPostDetail{Slug: item.Slug, Title: item.Title, Excerpt: item.Excerpt,
		Cover: item.Cover, CoverAlt: item.CoverAlt, Category: item.Category, Author: item.Author, Tags: item.Tags,
		Featured: item.Featured, PublishedAt: item.PublishedAt, Body: post.Body, Products: products,
		Related: blogPostItems(related)})
}

// postProducts is the post's visible products as cards, in the post's order.
func (s *Server) postProducts(r *http.Request, ids []uuid.UUID) ([]ProductListItem, error) {
	out := []ProductListItem{}
	if len(ids) == 0 {
		return out, nil
	}
	res, err := s.catalog.ShopProducts(r.Context(), catalog.ShopFilter{IDs: ids, Limit: len(ids)})
	if err != nil {
		return nil, err
	}
	byID := make(map[uuid.UUID]catalog.ShopProduct, len(res.Products))
	for _, p := range res.Products {
		byID[p.ID] = p
	}
	for _, id := range ids {
		if p, ok := byID[id]; ok {
			out = append(out, productListItem(p))
		}
	}
	return out, nil
}

func blogPostItems(posts []blog.ShopPost) []BlogPostListItem {
	out := make([]BlogPostListItem, 0, len(posts))
	for _, p := range posts {
		out = append(out, blogPostItem(p))
	}
	return out
}

// blogPostItem is a post card. The cover's alt text is the title, and a
// cover has no renditions yet (BR-135).
func blogPostItem(p blog.ShopPost) BlogPostListItem {
	item := BlogPostListItem{Slug: p.Slug, Title: p.Title, Excerpt: p.Excerpt, CoverAlt: p.Title, Tags: p.Tags,
		Featured: p.Featured, PublishedAt: p.PublishedAt}
	if p.CoverURL != nil {
		item.Cover = &Image{Url: *p.CoverURL, Sizes: map[string]string{}}
	}
	if p.CategorySlug != nil && p.CategoryName != nil {
		item.Category = &BlogCategoryRef{Slug: *p.CategorySlug, Name: *p.CategoryName}
	}
	if p.AuthorName != nil {
		item.Author = &BlogAuthor{Name: *p.AuthorName, Bio: p.AuthorBio}
	}
	return item
}
