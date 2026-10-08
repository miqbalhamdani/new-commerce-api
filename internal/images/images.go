// Package images makes the WebP derivatives of product images (BR-052,
// P1-044). It links libvips through cgo, so only cmd/worker imports it and the
// API binary stays pure Go.
package images

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"strconv"
	"strings"
	"sync"

	"github.com/davidbyttow/govips/v2/vips"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/db/sqlcgen"
	"github.com/miqbalhamdani/new-commerce-api/internal/jobs"
	"github.com/miqbalhamdani/new-commerce-api/internal/storage"
)

// Widths are the derivative sizes, in px (BR-052).
var Widths = []int{1600, 800, 200}

// maxBytes matches the upload limit (BR-051); anything larger was refused at
// confirm.
const maxBytes = 20 << 20

var startOnce sync.Once

// Derive returns a WebP of buf at each of Widths, keyed by width, and the
// image's own size with its EXIF orientation applied. A narrower image is not
// enlarged.
func Derive(buf []byte) (out map[string][]byte, width, height int, err error) {
	startOnce.Do(func() {
		vips.LoggingSettings(nil, vips.LogLevelError)
		err = vips.Startup(nil)
	})
	if err != nil {
		return nil, 0, 0, fmt.Errorf("start libvips: %w", err)
	}

	img, err := vips.NewImageFromBuffer(buf)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("decode image: %w", err)
	}
	width, height = img.Width(), img.Height()
	if o := img.Orientation(); o >= 5 && o <= 8 { // rotated a quarter turn
		width, height = height, width
	}
	img.Close()

	out = map[string][]byte{}
	for _, w := range Widths {
		// The height bound is never the one that binds: width alone sets the size.
		t, err := vips.NewThumbnailWithSizeFromBuffer(buf, w, 10*w, vips.InterestingNone, vips.SizeDown)
		if err != nil {
			return nil, 0, 0, fmt.Errorf("resize to %d: %w", w, err)
		}
		p := vips.NewWebpExportParams()
		p.StripMetadata, p.Quality = true, 80
		b, _, err := t.ExportWebp(p)
		t.Close()
		if err != nil {
			return nil, 0, 0, fmt.Errorf("encode %d: %w", w, err)
		}
		out[strconv.Itoa(w)] = b
	}
	return out, width, height, nil
}

// Key is where a derivative of original lives: next to it, under the same
// public products/ prefix, e.g. {tenant}/products/{product}/{sha}_800.webp.
func Key(original, width string) string {
	return strings.TrimSuffix(original, path.Ext(original)) + "_" + width + ".webp"
}

// Handler is the image_derivatives job (P1-044). Running it twice is safe: an
// image that already has derivatives, or no longer exists, is left alone.
func Handler(store *db.Store, files *storage.Store) jobs.Handler {
	return func(ctx context.Context, job jobs.Job, _ jobs.Progress) (any, error) {
		var p struct {
			MediaID uuid.UUID `json:"media_id"`
		}
		if err := json.Unmarshal(job.Params, &p); err != nil {
			return nil, err
		}
		var m sqlcgen.ProductMedium
		err := store.InTenantTx(ctx, func(tx pgx.Tx) (err error) {
			m, err = sqlcgen.New(tx).GetMedia(ctx, p.MediaID)
			return err
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil // deleted before the worker got to it
		}
		if err != nil {
			return nil, err
		}
		done := map[string]string{}
		if json.Unmarshal(m.Derivatives, &done) == nil && len(done) > 0 {
			return nil, nil
		}

		obj, err := files.Get(ctx, m.R2Key)
		if err != nil {
			return nil, err
		}
		buf, err := io.ReadAll(io.LimitReader(obj, maxBytes+1))
		_ = obj.Close()
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", m.R2Key, err)
		}
		renditions, w, h, err := Derive(buf)
		if err != nil {
			return nil, err
		}

		keys := map[string]string{}
		for size, b := range renditions {
			key := Key(m.R2Key, size)
			if err := files.Put(ctx, key, bytes.NewReader(b), int64(len(b)), "image/webp"); err != nil {
				return nil, err
			}
			keys[size] = key
		}
		raw, _ := json.Marshal(keys)
		w32, h32 := int32(w), int32(h)
		var updated int64
		err = store.InTenantTx(ctx, func(tx pgx.Tx) (err error) {
			updated, err = sqlcgen.New(tx).SetMediaDerivatives(ctx, sqlcgen.SetMediaDerivativesParams{
				ID: m.ID, Derivatives: raw, Width: &w32, Height: &h32})
			return err
		})
		if err != nil {
			return nil, err
		}
		if updated == 0 { // deleted while resizing: its delete could not see these
			for _, key := range keys {
				if err := files.Delete(ctx, key); err != nil {
					slog.WarnContext(ctx, "orphaned derivative", "error", err)
				}
			}
		}
		return map[string]any{"media_id": m.ID, "width": w, "height": h}, nil
	}
}
