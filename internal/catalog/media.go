package catalog

import (
	"context"

	"github.com/google/uuid"

	"github.com/miqbalhamdani/new-commerce-api/internal/db/sqlcgen"
)

// Media is one product image (04-api-spec.md §8).
type Media struct{}

// productMedia lists a product's images in position order.
//
// ponytail: empty until product_media exists (P1-042/043).
func productMedia(_ context.Context, _ *sqlcgen.Queries, _ uuid.UUID) ([]Media, error) {
	return []Media{}, nil
}
