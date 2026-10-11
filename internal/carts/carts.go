// Package carts is the storefront's server-side cart (P1-204, 04-api-spec.md
// §11.5). A cart's random id is its credential (BR-087); it stores quantities
// and never prices, which are read now through the storefront views
// (BR-046, BR-081).
package carts

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/db/sqlcgen"
	apperrors "github.com/miqbalhamdani/new-commerce-api/internal/platform/errors"
	"github.com/miqbalhamdani/new-commerce-api/internal/storage"
)

type Service struct {
	store *db.Store
	files *storage.Store
}

func NewService(store *db.Store, files *storage.Store) *Service {
	return &Service{store: store, files: files}
}

// Cart is a cart priced now.
type Cart struct {
	ID        uuid.UUID
	Items     []Item
	ItemCount int
	Subtotal  int64
	ExpiresAt time.Time
}

// Item is one variant in a cart. An item that is no longer available keeps
// its name and has no price (BR-090).
type Item struct {
	VariantID    uuid.UUID
	SKU          *string
	Title        string
	ProductSlug  string
	OptionValues []string
	Qty          int
	UnitPrice    *int64
	LineTotal    *int64
	ImageURL     *string
	Available    bool
}

// Create starts an empty cart. Its id is a random v4, not the usual v7: the
// id is the cart's bearer credential, and a v7's leading bits are a timestamp
// (BR-005).
// A signed-in shopper's cart is theirs from the start (BR-095).
func (s *Service) Create(ctx context.Context, customerID *uuid.UUID) (Cart, error) {
	var c Cart
	err := s.tx(ctx, func(q *sqlcgen.Queries) error {
		row, err := q.CreateCart(ctx, sqlcgen.CreateCartParams{ID: uuid.New(), CustomerID: customerID})
		if err != nil {
			return err
		}
		c = Cart{ID: row.ID, Items: []Item{}, ExpiresAt: row.ExpiresAt}
		return nil
	})
	return c, err
}

// Get is an open cart, priced now. Unknown, expired and checked-out carts
// are all 404 (§11.5). A signed-in shopper claims a cart nobody owns yet.
func (s *Service) Get(ctx context.Context, id uuid.UUID, customerID *uuid.UUID) (Cart, error) {
	var c Cart
	err := s.tx(ctx, func(q *sqlcgen.Queries) error {
		row, err := q.GetCart(ctx, id)
		if err := open(row, err); err != nil {
			return err
		}
		if err := claim(ctx, q, id, customerID); err != nil {
			return err
		}
		c, err = s.priced(ctx, q, row)
		return err
	})
	return c, err
}

// SetItem sets a variant's quantity; 0 removes it, even a variant no longer
// visible, so a shopper can always clear one (BR-090). Adding anything else
// needs a visible variant (BR-080). The cart's row lock serialises this with
// checkout: an edit that waited on one finds the cart checked out.
func (s *Service) SetItem(ctx context.Context, id, variantID uuid.UUID, qty int, customerID *uuid.UUID) (Cart, error) {
	if qty < 0 || qty > 999 {
		return Cart{}, fieldError("qty", "qty is 0 to 999; 0 removes the item")
	}
	var c Cart
	err := s.tx(ctx, func(q *sqlcgen.Queries) error {
		row, err := q.LockCart(ctx, id)
		if err := open(row, err); err != nil {
			return err
		}
		if err := claim(ctx, q, id, customerID); err != nil {
			return err
		}
		if qty == 0 {
			err = q.DeleteCartItem(ctx, sqlcgen.DeleteCartItemParams{CartID: id, VariantID: variantID})
		} else {
			var visible bool
			if visible, err = q.VariantVisible(ctx, variantID); err == nil && !visible {
				return fieldError("variant_id", "This variant is not for sale.")
			}
			if err == nil {
				err = q.SetCartItem(ctx, sqlcgen.SetCartItemParams{CartID: id, VariantID: variantID, Qty: int32(qty)})
			}
		}
		if err != nil {
			return err
		}
		if err := q.TouchCart(ctx, id); err != nil {
			return err
		}
		if row, err = q.GetCart(ctx, id); err != nil {
			return err
		}
		c, err = s.priced(ctx, q, row)
		return err
	})
	return c, err
}

// claim gives an unowned cart to the signed-in shopper, so a cart started as
// a guest follows them after sign-in; it never takes one from another
// customer (BR-095).
func claim(ctx context.Context, q *sqlcgen.Queries, id uuid.UUID, customerID *uuid.UUID) error {
	if customerID == nil {
		return nil
	}
	return q.ClaimCart(ctx, sqlcgen.ClaimCartParams{ID: id, CustomerID: customerID})
}

// open turns a missing, expired or checked-out cart into the one 404 they all
// are to a shopper.
func open(row sqlcgen.Cart, err error) error {
	if errors.Is(err, pgx.ErrNoRows) || err == nil && (row.Status != "open" || !row.ExpiresAt.After(time.Now())) {
		return apperrors.NotFound("No such cart.")
	}
	return err
}

func (s *Service) priced(ctx context.Context, q *sqlcgen.Queries, row sqlcgen.Cart) (Cart, error) {
	lines, err := q.CartLines(ctx, row.ID)
	if err != nil {
		return Cart{}, err
	}
	images, err := q.CartImages(ctx, row.ID)
	if err != nil {
		return Cart{}, err
	}
	imageOf := make(map[uuid.UUID]string, len(images))
	for _, im := range images {
		imageOf[im.VariantID] = s.files.PublicURL(im.ImageKey)
	}

	c := Cart{ID: row.ID, Items: make([]Item, 0, len(lines)), ExpiresAt: row.ExpiresAt}
	for _, l := range lines {
		it := Item{VariantID: l.VariantID, SKU: l.Sku, Title: Title(l.ProductTitle, l.OptionValues),
			ProductSlug: l.ProductSlug, OptionValues: l.OptionValues, Qty: int(l.Qty),
			Available: l.Available != nil && *l.Available && l.PriceAmount != nil}
		if u, ok := imageOf[l.VariantID]; ok {
			it.ImageURL = &u
		}
		if it.Available {
			total := *l.PriceAmount * int64(l.Qty)
			it.UnitPrice, it.LineTotal = l.PriceAmount, &total
			c.Subtotal += total
		}
		c.ItemCount += it.Qty
		c.Items = append(c.Items, it)
	}
	return c, nil
}

// Title is what a cart line and an order line call a variant, 'Erigo Basic
// Tee — Black / M' (03-erd.md §3.6).
func Title(product string, optionValues []string) string {
	if len(optionValues) == 0 {
		return product
	}
	return product + " — " + strings.Join(optionValues, " / ")
}

func (s *Service) tx(ctx context.Context, fn func(q *sqlcgen.Queries) error) error {
	return s.store.InTenantTx(ctx, func(tx pgx.Tx) error { return fn(sqlcgen.New(tx)) })
}

func fieldError(field, detail string) error {
	return apperrors.ValidationFailed(detail).WithFields(apperrors.Field{Name: field, Detail: detail})
}
