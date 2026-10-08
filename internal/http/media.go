package httpapi

import (
	"net/http"

	"github.com/google/uuid"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/catalog"
	apperrors "github.com/miqbalhamdani/new-commerce-api/internal/platform/errors"
)

// Media: 04-api-spec.md §8 (P1-043). Bytes never pass through here (BR-051).

type presignBody struct {
	Purpose   optional[string]    `json:"purpose"`
	ProductID optional[uuid.UUID] `json:"product_id"`
	MimeType  optional[string]    `json:"mime_type"`
	Bytes     optional[int64]     `json:"bytes"`
	SHA256    optional[string]    `json:"sha256"`
}

// PresignMedia needs media:write for an image and products:write for an
// import; the purpose decides which, so it is checked after decoding.
func (s *Server) PresignMedia(w http.ResponseWriter, r *http.Request) {
	role, ok := auth.RoleFromContext(r.Context())
	if !ok {
		writeError(w, r, apperrors.Unauthenticated("A bearer token is required."))
		return
	}
	var body presignBody
	if !decodeJSON(w, r, &body) {
		return
	}
	for name, sent := range map[string]bool{"purpose": body.Purpose.Set, "mime_type": body.MimeType.Set,
		"bytes": body.Bytes.Set, "sha256": body.SHA256.Set} {
		if !sent {
			writeError(w, r, fieldErr(name, name+" is required"))
			return
		}
	}
	if err := rejectNull(map[string]bool{"purpose": body.Purpose.Null, "product_id": body.ProductID.Null,
		"mime_type": body.MimeType.Null, "bytes": body.Bytes.Null, "sha256": body.SHA256.Null}); err != nil {
		writeError(w, r, err)
		return
	}
	if !sha256Pattern.MatchString(body.SHA256.Value) {
		writeError(w, r, fieldErr("sha256", "sha256 is 64 lower-case hex characters"))
		return
	}
	if body.Bytes.Value < 1 {
		writeError(w, r, fieldErr("bytes", "bytes must be positive"))
		return
	}

	var p catalog.Presign
	var err error
	switch body.Purpose.Value {
	case "product_image":
		if !auth.Can(role, auth.PermMediaWrite) {
			writeError(w, r, apperrors.PermissionDenied(auth.PermMediaWrite))
			return
		}
		if !body.ProductID.Set {
			writeError(w, r, fieldErr("product_id", "product_id is required for a product image"))
			return
		}
		p, err = s.catalog.PresignImage(r.Context(), body.ProductID.Value, body.MimeType.Value, body.Bytes.Value, body.SHA256.Value)
	case "product_import":
		if !auth.Can(role, auth.PermProductsWrite) {
			writeError(w, r, apperrors.PermissionDenied(auth.PermProductsWrite))
			return
		}
		if body.ProductID.Set {
			writeError(w, r, fieldErr("product_id", "an import belongs to no product"))
			return
		}
		p, err = s.catalog.PresignImport(r.Context(), body.MimeType.Value, body.Bytes.Value)
	default:
		writeError(w, r, fieldErr("purpose", "purpose is product_image or product_import"))
		return
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, PresignResponse{UploadUrl: p.URL, R2Key: p.Key, ExpiresIn: p.ExpiresIn})
}

type confirmBody struct {
	R2Key     optional[string]    `json:"r2_key"`
	ProductID optional[uuid.UUID] `json:"product_id"`
	VariantID optional[uuid.UUID] `json:"variant_id"`
}

func (s *Server) ConfirmMedia(w http.ResponseWriter, r *http.Request) {
	requirePermission(auth.PermMediaWrite, func(w http.ResponseWriter, r *http.Request) {
		var body confirmBody
		if !decodeJSON(w, r, &body) {
			return
		}
		if !body.R2Key.Set || body.R2Key.Null || !body.ProductID.Set || body.ProductID.Null {
			writeError(w, r, fieldErr("r2_key", "r2_key and product_id are required"))
			return
		}
		var variant *uuid.UUID
		if body.VariantID.Set && !body.VariantID.Null {
			variant = &body.VariantID.Value
		}
		m, err := s.catalog.ConfirmMedia(r.Context(), body.ProductID.Value, body.R2Key.Value, variant)
		if err != nil {
			writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusCreated, mediaItem(m))
	})(w, r)
}

func (s *Server) UpdateMedia(w http.ResponseWriter, r *http.Request, id Id) {
	requirePermission(auth.PermMediaWrite, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			VariantID optional[uuid.UUID] `json:"variant_id"`
		}
		if !decodeJSON(w, r, &body) {
			return
		}
		if !body.VariantID.Set {
			writeError(w, r, fieldErr("variant_id", "variant_id is required; null makes the image product-level"))
			return
		}
		var variant *uuid.UUID
		if !body.VariantID.Null {
			variant = &body.VariantID.Value
		}
		m, err := s.catalog.AttachMedia(r.Context(), id, variant)
		if err != nil {
			writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, mediaItem(m))
	})(w, r)
}

func (s *Server) DeleteMedia(w http.ResponseWriter, r *http.Request, id Id) {
	requirePermission(auth.PermMediaWrite, func(w http.ResponseWriter, r *http.Request) {
		if err := s.catalog.DeleteMedia(r.Context(), id); err != nil {
			writeError(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})(w, r)
}

func (s *Server) OrderMedia(w http.ResponseWriter, r *http.Request, id Id) {
	requirePermission(auth.PermMediaWrite, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			MediaIDs optional[[]uuid.UUID] `json:"media_ids"`
		}
		if !decodeJSON(w, r, &body) {
			return
		}
		if !body.MediaIDs.Set || body.MediaIDs.Null {
			writeError(w, r, fieldErr("media_ids", "media_ids is required"))
			return
		}
		ms, err := s.catalog.OrderMedia(r.Context(), id, body.MediaIDs.Value)
		if err != nil {
			writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, MediaList{Data: mediaOut(ms)})
	})(w, r)
}

func mediaOut(ms []catalog.Media) []Media {
	out := make([]Media, 0, len(ms))
	for _, m := range ms {
		out = append(out, mediaItem(m))
	}
	return out
}

func mediaItem(m catalog.Media) Media {
	out := Media{Id: m.ID, ProductId: m.ProductID, VariantId: m.VariantID, MimeType: m.MimeType, Bytes: m.Bytes,
		Position: int(m.Position), Url: m.URL, Derivatives: m.Derivatives, CreatedAt: m.CreatedAt}
	if m.Width != nil {
		w := int(*m.Width)
		out.Width = &w
	}
	if m.Height != nil {
		h := int(*m.Height)
		out.Height = &h
	}
	return out
}
