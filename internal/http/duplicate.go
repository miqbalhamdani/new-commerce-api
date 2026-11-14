package httpapi

import (
	"net/http"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
)

// DuplicateProduct handles POST /v1/products/{id}/duplicate (P1-939, §7.1):
// a draft copy with the same details, variants and images (BR-047).
func (s *Server) DuplicateProduct(w http.ResponseWriter, r *http.Request, id Id) {
	requirePermission(auth.PermProductsWrite, requirePermission(auth.PermVariantsWrite,
		requirePermission(auth.PermMediaWrite, func(w http.ResponseWriter, r *http.Request) {
			p, err := s.catalog.DuplicateProduct(r.Context(), id)
			if err != nil {
				writeError(w, r, err)
				return
			}
			writeJSON(w, http.StatusCreated, productOut(p))
		})))(w, r)
}
