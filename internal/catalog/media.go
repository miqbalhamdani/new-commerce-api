package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/db/sqlcgen"
	"github.com/miqbalhamdani/new-commerce-api/internal/jobs"
	"github.com/miqbalhamdani/new-commerce-api/internal/storage"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

// Upload limits (BR-051, 04-api-spec.md §8).
const (
	maxImageBytes  = 20 << 20
	maxImportBytes = 50 << 20
	presignTTL     = 10 * time.Minute
)

var imageTypes = map[string]string{"image/jpeg": "jpg", "image/png": "png", "image/webp": "webp"}

var importTypes = map[string]bool{"text/csv": true, "application/csv": true, "text/plain": true,
	"application/vnd.ms-excel": true}

// Media is one image with its URLs built from its keys (BR-050).
type Media struct {
	sqlcgen.ProductMedium
	URL         string
	Derivatives map[string]string
}

// Presign is a signed direct upload.
type Presign struct {
	URL       string
	Key       string
	ExpiresIn int
}

// PresignImage signs an upload of a product image to
// {tenant}/products/{product}/{sha256}.{ext} (BR-053).
func (s *Service) PresignImage(ctx context.Context, productID uuid.UUID, mime string, bytes int64, sha string) (Presign, error) {
	ext, ok := imageTypes[mime]
	if !ok {
		return Presign{}, fieldError("mime_type", "images are image/jpeg, image/png or image/webp")
	}
	if bytes > maxImageBytes {
		return Presign{}, fieldError("bytes", "an image is at most 20 MB")
	}
	if err := s.tx(ctx, func(q *sqlcgen.Queries, _ pgx.Tx) error { return liveProduct(ctx, q, productID) }); err != nil {
		return Presign{}, err
	}
	return s.presign(ctx, fmt.Sprintf("%s/products/%s/%s.%s", tenantOf(ctx), productID, sha, ext))
}

// PresignImport signs an upload of a CSV to {tenant}/jobs/{job}/upload.csv;
// the import job takes that id when it is created (P1-073).
func (s *Service) PresignImport(ctx context.Context, mime string, bytes int64) (Presign, error) {
	if !importTypes[mime] {
		return Presign{}, fieldError("mime_type", "an import is a CSV file")
	}
	if bytes > maxImportBytes {
		return Presign{}, fieldError("bytes", "an import is at most 50 MB")
	}
	return s.presign(ctx, fmt.Sprintf("%s/jobs/%s/upload.csv", tenantOf(ctx), uuid.Must(uuid.NewV7())))
}

func (s *Service) presign(ctx context.Context, key string) (Presign, error) {
	u, err := s.files.PresignPut(ctx, key, presignTTL)
	return Presign{URL: u, Key: key, ExpiresIn: int(presignTTL.Seconds())}, err
}

// ConfirmMedia records an uploaded image after checking the object itself:
// it exists, it is an image, it is not too big (BR-051). Derivatives are
// queued for the worker (BR-052).
func (s *Service) ConfirmMedia(ctx context.Context, productID uuid.UUID, key string, variant *uuid.UUID) (Media, error) {
	if !strings.HasPrefix(key, fmt.Sprintf("%s/products/%s/", tenantOf(ctx), productID)) {
		return Media{}, fieldError("r2_key", "the key is not an upload for this product")
	}
	mime, size, err := s.files.Head(ctx, key)
	if errors.Is(err, storage.ErrNotFound) {
		return Media{}, fieldError("r2_key", "nothing was uploaded under this key")
	}
	if err != nil {
		return Media{}, err
	}
	if _, ok := imageTypes[mime]; !ok {
		return Media{}, fieldError("r2_key", "the uploaded object is "+mime+", not an image")
	}
	if size > maxImageBytes {
		return Media{}, fieldError("r2_key", "the uploaded image is over 20 MB")
	}

	var m sqlcgen.ProductMedium
	var job *jobs.Job
	err = s.tx(ctx, func(q *sqlcgen.Queries, tx pgx.Tx) error {
		if err := liveProduct(ctx, q, productID); err != nil {
			return err
		}
		if err := variantOf(ctx, q, productID, variant); err != nil {
			return err
		}
		created, err := q.CreateMedia(ctx, sqlcgen.CreateMediaParams{ID: uuid.Must(uuid.NewV7()), ProductID: productID,
			VariantID: variant, R2Key: key, MimeType: mime, Bytes: size})
		if errors.Is(err, pgx.ErrNoRows) { // confirmed before: same image
			m, err = q.GetMediaByKey(ctx, sqlcgen.GetMediaByKeyParams{ProductID: productID, R2Key: key})
			return err
		}
		if err != nil {
			return err
		}
		m = created
		j, err := jobs.Create(ctx, tx, "image_derivatives", map[string]string{"media_id": m.ID.String()}, actorOf(ctx))
		if err != nil {
			return err
		}
		job = &j
		return db.Audit(ctx, tx, db.AuditEntry{Action: "media.create", SubjectType: "media", SubjectID: m.ID.String(),
			After: map[string]any{"product_id": productID, "variant_id": variant, "r2_key": key}})
	})
	if err != nil {
		return Media{}, err
	}
	if job != nil {
		if err := s.jobs.Enqueue(ctx, *job); err != nil {
			slog.ErrorContext(ctx, "enqueue derivatives", "error", err)
		}
	}
	return s.mediaView(m), nil
}

// AttachMedia moves an image to a variant of its product, or back to the
// product with nil.
func (s *Service) AttachMedia(ctx context.Context, id uuid.UUID, variant *uuid.UUID) (Media, error) {
	var m sqlcgen.ProductMedium
	err := s.tx(ctx, func(q *sqlcgen.Queries, tx pgx.Tx) error {
		before, err := q.GetMedia(ctx, id)
		if err != nil {
			return notFound(err, "image")
		}
		if err := variantOf(ctx, q, before.ProductID, variant); err != nil {
			return err
		}
		if m, err = q.SetMediaVariant(ctx, sqlcgen.SetMediaVariantParams{ID: id, VariantID: variant}); err != nil {
			return err
		}
		return db.Audit(ctx, tx, db.AuditEntry{Action: "media.update", SubjectType: "media", SubjectID: id.String(),
			Before: map[string]any{"variant_id": before.VariantID}, After: map[string]any{"variant_id": variant}})
	})
	return s.mediaView(m), err
}

// DeleteMedia removes the row (media is the one thing truly deleted, BR-012)
// and then its objects, unless another row still uses the key.
func (s *Service) DeleteMedia(ctx context.Context, id uuid.UUID) error {
	var gone sqlcgen.ProductMedium
	var inUse bool
	err := s.tx(ctx, func(q *sqlcgen.Queries, tx pgx.Tx) (err error) {
		if gone, err = q.DeleteMedia(ctx, id); err != nil {
			return notFound(err, "image")
		}
		if inUse, err = q.MediaKeyInUse(ctx, gone.R2Key); err != nil {
			return err
		}
		return db.Audit(ctx, tx, db.AuditEntry{Action: "media.delete", SubjectType: "media", SubjectID: id.String(),
			Before: map[string]any{"product_id": gone.ProductID, "r2_key": gone.R2Key}})
	})
	if err != nil || inUse {
		return err
	}
	// ponytail: deleted inline after commit, not by the worker; a failure
	// leaves an orphan object, logged. A sweeper can collect those later.
	keys := []string{gone.R2Key}
	for _, k := range derivativeKeys(gone.Derivatives) {
		keys = append(keys, k)
	}
	for _, k := range keys {
		if err := s.files.Delete(context.WithoutCancel(ctx), k); err != nil {
			slog.WarnContext(ctx, "orphaned object after media delete", "error", err)
		}
	}
	return nil
}

// OrderMedia sets a product's image order. ids must be exactly its images.
func (s *Service) OrderMedia(ctx context.Context, productID uuid.UUID, ids []uuid.UUID) ([]Media, error) {
	var out []Media
	err := s.tx(ctx, func(q *sqlcgen.Queries, tx pgx.Tx) error {
		if _, err := q.GetProduct(ctx, productID); err != nil {
			return notFound(err, "product")
		}
		current, err := q.ListProductMedia(ctx, productID)
		if err != nil {
			return err
		}
		have := make([]string, 0, len(current))
		for _, m := range current {
			have = append(have, m.ID.String())
		}
		want := make([]string, 0, len(ids))
		for _, id := range ids {
			want = append(want, id.String())
		}
		if !slices.Equal(slices.Sorted(slices.Values(have)), slices.Sorted(slices.Values(want))) {
			return fieldError("media_ids", "media_ids must list every image of the product exactly once")
		}
		for i, id := range ids {
			if err := q.SetMediaPosition(ctx, sqlcgen.SetMediaPositionParams{ID: id, Position: int32(i)}); err != nil {
				return err
			}
		}
		if out, err = productMedia(ctx, q, s, productID); err != nil {
			return err
		}
		return db.Audit(ctx, tx, db.AuditEntry{Action: "product.media_order", SubjectType: "product",
			SubjectID: productID.String(), Before: map[string]any{"media_ids": have}, After: map[string]any{"media_ids": want}})
	})
	return out, err
}

// productMedia lists a product's images in position order.
func productMedia(ctx context.Context, q *sqlcgen.Queries, s *Service, productID uuid.UUID) ([]Media, error) {
	rows, err := q.ListProductMedia(ctx, productID)
	out := make([]Media, 0, len(rows))
	for _, m := range rows {
		out = append(out, s.mediaView(m))
	}
	return out, err
}

func (s *Service) mediaView(m sqlcgen.ProductMedium) Media {
	v := Media{ProductMedium: m, Derivatives: map[string]string{}}
	if m.R2Key == "" {
		return v
	}
	v.URL = s.files.PublicURL(m.R2Key)
	for size, key := range derivativeKeys(m.Derivatives) {
		v.Derivatives[size] = s.files.PublicURL(key)
	}
	return v
}

func derivativeKeys(raw []byte) map[string]string {
	out := map[string]string{}
	_ = json.Unmarshal(raw, &out)
	return out
}

func liveProduct(ctx context.Context, q *sqlcgen.Queries, id uuid.UUID) error {
	p, err := q.GetProduct(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && p.ArchivedAt != nil) {
		return fieldError("product_id", "no such live product")
	}
	return err
}

// variantOf requires the variant, when given, to be a live variant of the
// image's product -- the FK only holds it to the tenant.
func variantOf(ctx context.Context, q *sqlcgen.Queries, productID uuid.UUID, variant *uuid.UUID) error {
	if variant == nil {
		return nil
	}
	ok, err := q.LiveVariantOfProduct(ctx, sqlcgen.LiveVariantOfProductParams{ID: *variant, ProductID: productID})
	if err == nil && !ok {
		return fieldError("variant_id", "the variant is not a live variant of this product")
	}
	return err
}

func tenantOf(ctx context.Context) uuid.UUID {
	id, _ := tenant.FromContext(ctx)
	return id
}

func actorOf(ctx context.Context) *uuid.UUID {
	if a, ok := tenant.ActorFromContext(ctx); ok {
		return &a.UserID
	}
	return nil
}
