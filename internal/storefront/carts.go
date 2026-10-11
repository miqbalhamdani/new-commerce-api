package storefront

import (
	"bytes"
	"io"
	"net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/miqbalhamdani/new-commerce-api/internal/carts"
	"github.com/miqbalhamdani/new-commerce-api/internal/httpkit"
)

// Carts: 04-api-spec.md §11.5 (P1-204). The cart id in the path is the
// credential (BR-087).

func (s *Server) CreateCart(w http.ResponseWriter, r *http.Request) {
	// No body is the normal call. One that is sent is still held to the rule
	// that a field the route does not define is 422, never ignored (BR-089).
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<10))
	if err != nil || len(bytes.TrimSpace(raw)) > 0 {
		r.Body = io.NopCloser(bytes.NewReader(raw))
		var none struct{}
		if !httpkit.DecodeJSON(w, r, &none) {
			return
		}
	}
	c, err := s.carts.Create(r.Context(), signedIn(r))
	if err != nil {
		httpkit.WriteError(w, r, err)
		return
	}
	httpkit.WriteJSON(w, http.StatusCreated, cartOut(c))
}

func (s *Server) GetCart(w http.ResponseWriter, r *http.Request, cartID CartId) {
	c, err := s.carts.Get(r.Context(), cartID, signedIn(r))
	if err != nil {
		httpkit.WriteError(w, r, err)
		return
	}
	httpkit.WriteJSON(w, http.StatusOK, cartOut(c))
}

func (s *Server) SetCartItem(w http.ResponseWriter, r *http.Request, cartID CartId, variantID openapi_types.UUID) {
	var body struct {
		Qty *int `json:"qty"`
	}
	if !httpkit.DecodeJSON(w, r, &body) {
		return
	}
	if body.Qty == nil {
		httpkit.WriteError(w, r, fieldError("qty", "qty is required: 1 to 999, or 0 to remove the item"))
		return
	}
	c, err := s.carts.SetItem(r.Context(), cartID, variantID, *body.Qty, signedIn(r))
	if err != nil {
		httpkit.WriteError(w, r, err)
		return
	}
	httpkit.WriteJSON(w, http.StatusOK, cartOut(c))
}

func cartOut(c carts.Cart) Cart {
	out := Cart{CartId: c.ID, Items: make([]CartItem, 0, len(c.Items)), ItemCount: c.ItemCount,
		Subtotal: c.Subtotal, ExpiresAt: c.ExpiresAt}
	for _, it := range c.Items {
		opts := it.OptionValues
		if opts == nil {
			opts = []string{}
		}
		out.Items = append(out.Items, CartItem{VariantId: it.VariantID, Sku: it.SKU, Title: it.Title,
			ProductSlug: it.ProductSlug, OptionValues: opts, Qty: it.Qty, UnitPrice: it.UnitPrice,
			LineTotal: it.LineTotal, ImageUrl: it.ImageURL, Available: it.Available})
	}
	return out
}
