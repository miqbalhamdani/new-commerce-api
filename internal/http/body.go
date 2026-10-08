package httpapi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	apperrors "github.com/miqbalhamdani/new-commerce-api/internal/platform/errors"
)

// maxBody caps a JSON request body. The largest legitimate one is a 500-row
// bulk upsert (P1-072); anything near this is a mistake or an attack.
const maxBody = 4 << 20

// serverManaged are the fields no client may send, on create or update alike
// (BR-008). Each route adds its own derived fields (a brand's slug).
var serverManaged = []string{"id", "tenant_id", "version", "created_at", "updated_at", "archived_at", "path"}

// decodeJSON decodes a request body into v, answering 422 itself on failure.
//
// In order: a server-managed field is validation_failed naming it (BR-008); a
// field v does not define is unknown_field naming it (04-api-spec.md §1.1);
// anything else that fails -- malformed JSON, a bad email, a timestamp with no
// offset (BR-007) -- is validation_failed.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any, alsoManaged ...string) bool {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		writeError(w, r, apperrors.ValidationFailed("The request body could not be read.").WithCause(err))
		return false
	}

	var keys map[string]json.RawMessage
	if json.Unmarshal(raw, &keys) == nil {
		for _, k := range append(serverManaged, alsoManaged...) {
			if _, sent := keys[k]; sent {
				writeError(w, r, apperrors.ValidationFailed(k+" is set by the server and cannot be sent.").
					WithFields(apperrors.Field{Name: k, Detail: "server-managed"}))
				return false
			}
		}
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		// encoding/json has no typed error for this; the message is its API.
		if name, ok := strings.CutPrefix(err.Error(), "json: unknown field "); ok {
			writeError(w, r, apperrors.UnknownField(strings.Trim(name, `"`)).WithCause(err))
			return false
		}
		writeError(w, r, apperrors.ValidationFailed(
			"The request body is malformed or a field is not in the expected format.").WithCause(err))
		return false
	}
	return true
}

// optional is a request field that tells "omitted" from "null" from a value
// (BR-009). On create, Null is a 422; on PATCH, omitted leaves the column
// alone and Null clears it where the ERD allows.
type optional[T any] struct {
	Set   bool
	Null  bool
	Value T
}

func (o *optional[T]) UnmarshalJSON(b []byte) error {
	o.Set = true
	if string(b) == "null" {
		o.Null = true
		return nil
	}
	return json.Unmarshal(b, &o.Value)
}

// rejectNull is BR-009 for fields that may not be null: on create any field,
// on PATCH the non-nullable ones.
func rejectNull(fields map[string]bool) error {
	for name, isNull := range fields {
		if isNull {
			return apperrors.ValidationFailed(name + " cannot be null; omit it instead.").
				WithFields(apperrors.Field{Name: name, Detail: "null not allowed"})
		}
	}
	return nil
}

// writeJSON writes v with status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// encodeCursor and decodeCursor make an opaque page cursor from the last
// row's sort key. Opaque so clients cannot build one, and so the key can
// change without a contract change.
func encodeCursor(key any) *string {
	b, _ := json.Marshal(key)
	s := base64.RawURLEncoding.EncodeToString(b)
	return &s
}

func decodeCursor(c *string, key any) error {
	if c == nil || *c == "" {
		return nil
	}
	b, err := base64.RawURLEncoding.DecodeString(*c)
	if err == nil {
		err = json.Unmarshal(b, key)
	}
	if err != nil {
		return apperrors.ValidationFailed("cursor is not one this API issued.").
			WithFields(apperrors.Field{Name: "cursor", Detail: "invalid"})
	}
	return nil
}

// pageLimit is the requested page size, defaulting to 50 (04-api-spec.md §1).
// The generated binder has already rejected values outside 1-200.
func pageLimit(l *int) int {
	if l == nil {
		return 50
	}
	return *l
}

// fieldErr is a 422 naming one field.
func fieldErr(field, detail string) error {
	return apperrors.ValidationFailed(detail).WithFields(apperrors.Field{Name: field, Detail: detail})
}
