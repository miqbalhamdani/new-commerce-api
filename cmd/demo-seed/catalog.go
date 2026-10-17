package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"image/color"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/miqbalhamdani/new-commerce-api/internal/catalog"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

// The catalog goes through the catalog service end to end -- create, the
// variant matrix, upload and confirm, publish -- so the publish check
// (BR-038) really passes for every active product, and every photo becomes an
// image_derivatives job for the worker.

type catalogResult struct {
	brands   map[string]uuid.UUID
	cats     map[string]uuid.UUID
	products []seededProduct
	photos   int
}

type seededProduct struct {
	def      demoProduct
	id       uuid.UUID
	variants map[string]uuid.UUID // by option values joined with "/"
}

func (sd *seeder) seedCatalog(ctx context.Context, now time.Time) (catalogResult, error) {
	out := catalogResult{brands: map[string]uuid.UUID{}, cats: map[string]uuid.UUID{}}
	for _, name := range append(slices.Clone(brandNames), archivedBrand) {
		b, err := sd.catalog.CreateBrand(ctx, name, nil)
		if err != nil {
			return out, fmt.Errorf("brand %s: %w", name, err)
		}
		out.brands[name] = b.ID
	}
	for _, c := range categories {
		var parent *uuid.UUID
		if c.parent != "" {
			p := out.cats[c.parent]
			parent = &p
		}
		cat, err := sd.catalog.CreateCategory(ctx, c.name, c.kind, parent, nil)
		if err != nil {
			return out, fmt.Errorf("category %s: %w", c.name, err)
		}
		out.cats[c.key] = cat.ID
	}

	fs, err := newFaces()
	if err != nil {
		return out, err
	}
	barcode := 0
	for _, def := range products {
		p, photos, err := sd.product(ctx, fs, def, out, &barcode, now)
		if err != nil {
			return out, fmt.Errorf("%s: %w", def.title, err)
		}
		out.products = append(out.products, p)
		out.photos += photos
	}
	return out, nil
}

// product creates one product with its variants and photos, and publishes it
// unless it is to stay a draft. Archived products are published first: they
// sell for a while and are archived after the orders (retire).
func (sd *seeder) product(ctx context.Context, fs faces, def demoProduct, cat catalogResult, barcode *int, now time.Time) (seededProduct, int, error) {
	brand := cat.brands[def.brand]
	cats := make([]uuid.UUID, len(def.cats))
	for i, k := range def.cats {
		cats[i] = cat.cats[k]
	}
	view, err := sd.catalog.CreateProduct(ctx, catalog.ProductInput{Title: def.title, Description: &def.desc,
		BrandID: &brand, CategoryIDs: cats, Attributes: def.attrs})
	if err != nil {
		return seededProduct{}, 0, err
	}

	names, rows := matrix(def, barcode, now)
	res, err := sd.catalog.SaveMatrix(ctx, view.ID, int(view.Version), names, rows, false)
	if err != nil {
		return seededProduct{}, 0, err
	}
	p := seededProduct{def: def, id: view.ID, variants: map[string]uuid.UUID{}}
	for _, r := range res.Results {
		if r.VariantID == nil {
			return p, 0, fmt.Errorf("variant %v: %s: %s", r.OptionValues, r.Code, r.Detail)
		}
		p.variants[strings.Join(r.OptionValues, "/")] = *r.VariantID
	}

	photos := 0
	if def.gap != "photo" {
		// One card per colour, on that colour's first variant; the first is the
		// cover. Then a darker detail shot of the product as a whole.
		colours := def.colours
		if colours == nil {
			colours = []string{orDefault(def.tone, "Krem")}
		}
		for _, c := range colours {
			var variant *uuid.UUID
			if def.colours != nil {
				for _, r := range rows {
					if r.OptionValues[0] == c {
						id := p.variants[strings.Join(r.OptionValues, "/")]
						variant = &id
						break
					}
				}
			}
			if err := sd.photo(ctx, fs, p.id, def, "Warna: "+c, swatches[c], variant); err != nil {
				return p, photos, err
			}
			photos++
		}
		if err := sd.photo(ctx, fs, p.id, def, "Detail bahan", shade(swatches[colours[0]]), nil); err != nil {
			return p, photos, err
		}
		photos++
	}

	if def.state != "draft" {
		cur, err := sd.catalog.GetProduct(ctx, p.id)
		if err != nil {
			return p, photos, err
		}
		active := "active"
		if _, err := sd.catalog.UpdateProduct(ctx, p.id, int(cur.Version), catalog.ProductPatch{Status: &active}); err != nil {
			return p, photos, fmt.Errorf("publish: %w", err)
		}
	}
	return p, photos, nil
}

// matrix is the product's variant grid: Warna before Ukuran (BR-040), every
// combination, each with a SKU, a barcode, a price and a weight -- or, for a
// draft with a gap, the one field that keeps it from publishing.
func matrix(def demoProduct, barcode *int, now time.Time) ([]string, []catalog.MatrixRow) {
	var names []string
	var axes [][]string
	if def.colours != nil {
		names, axes = append(names, "Warna"), append(axes, def.colours)
	}
	if def.sizes != nil {
		names, axes = append(names, "Ukuran"), append(axes, def.sizes)
	}
	combos := [][]string{{}}
	for _, axis := range axes {
		var next [][]string
		for _, c := range combos {
			for _, v := range axis {
				next = append(next, append(slices.Clone(c), v))
			}
		}
		combos = next
	}

	rows := make([]catalog.MatrixRow, 0, len(combos))
	for _, opts := range combos {
		*barcode++
		price := def.price * 100
		if slices.Contains(opts, "XXL") {
			price += 1000000 // Rp10.000 more for the largest size
		}
		sku, code, weight := sku(def.sku, opts), ean13(*barcode), def.weight
		f := catalog.VariantFields{SKU: &sku, Barcode: &code, RegularPrice: &price, WeightGrams: &weight}
		switch def.sale {
		case saleNow:
			f.SalePrice, f.SaleStartsAt, f.SaleEndsAt = off(price, 15), at(now, -5*day), at(now, 9*day)
		case saleScheduled:
			f.SalePrice, f.SaleStartsAt, f.SaleEndsAt = off(price, 20), at(now, 3*day), at(now, 10*day)
		case saleOpen:
			f.SalePrice = off(price, 10)
		}
		rows = append(rows, catalog.MatrixRow{OptionValues: opts, Fields: f})
	}
	return names, rows
}

// photo draws a placeholder card, uploads it where the admin's presigned
// upload would put it (BR-053), and confirms it as the admin does.
func (sd *seeder) photo(ctx context.Context, fs faces, productID uuid.UUID, def demoProduct, caption string,
	bg color.RGBA, variant *uuid.UUID) error {
	data, err := fs.placeholder(def.brand, def.title, caption, bg)
	if err != nil {
		return err
	}
	tenantID, _ := tenant.FromContext(ctx)
	key := fmt.Sprintf("%s/products/%s/%x.png", tenantID, productID, sha256.Sum256(data))
	if err := sd.files.Put(ctx, key, bytes.NewReader(data), int64(len(data)), "image/png"); err != nil {
		return err
	}
	_, err = sd.catalog.ConfirmMedia(ctx, productID, key, variant)
	return err
}

// sku is the product's code and each option's: KPP-HIT-M.
func sku(prefix string, opts []string) string {
	parts := []string{prefix}
	for _, o := range opts {
		code := strings.ToUpper(strings.NewReplacer(" ", "", "/", "").Replace(o))
		if len(code) > 3 && strings.Trim(code, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") == "" {
			code = code[:3]
		}
		parts = append(parts, code)
	}
	return strings.Join(parts, "-")
}

// ean13 is an EAN-13 under Indonesia's 899 prefix, with its check digit.
func ean13(n int) string {
	s := fmt.Sprintf("899%09d", n)
	sum := 0
	for i, c := range s {
		d := int(c - '0')
		if i%2 == 1 {
			d *= 3
		}
		sum += d
	}
	return s + fmt.Sprint((10-sum%10)%10)
}

// off is price less pct percent, down to a whole thousand rupiah.
func off(price int64, pct int64) *int64 {
	p := price * (100 - pct) / 100 / 100000 * 100000
	return &p
}

func at(now time.Time, d time.Duration) *time.Time {
	t := now.Add(d)
	return &t
}

func orDefault(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// retire archives what a shop retires once orders exist: two products and a
// variant that sold for a while, a brand and a collection no longer used --
// and revokes the old website's API key.
func (sd *seeder) retire(t *teamCtx, cat catalogResult) error {
	ctx := t.as(t.admin)
	for _, p := range cat.products {
		if p.def.title == archivedVariant.product {
			id, ok := p.variants[strings.Join(archivedVariant.options, "/")]
			if !ok {
				return fmt.Errorf("%s has no variant %v", p.def.title, archivedVariant.options)
			}
			if err := sd.catalog.ArchiveVariant(ctx, id); err != nil {
				return err
			}
		}
		if p.def.state == "archived" {
			if err := sd.catalog.ArchiveProduct(ctx, p.id); err != nil {
				return fmt.Errorf("%s: %w", p.def.title, err)
			}
		}
	}
	if err := sd.catalog.ArchiveBrand(ctx, cat.brands[archivedBrand]); err != nil {
		return err
	}
	if err := sd.catalog.ArchiveCategory(ctx, cat.cats["promo"]); err != nil {
		return err
	}
	return sd.team.RevokeAPIKey(t.as(t.owner), t.oldKey)
}
