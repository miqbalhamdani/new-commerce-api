package httpapi

import (
	"net/http"

	apperrors "github.com/miqbalhamdani/new-commerce-api/internal/platform/errors"
)

// DuplicateProduct handles POST /v1/products/{id}/duplicate (BR-047). It is
// built by P1-939; until then the route answers 404 like an unknown product.
func (s *Server) DuplicateProduct(w http.ResponseWriter, r *http.Request, id Id) {
	writeError(w, r, apperrors.NotFound("product"))
}
