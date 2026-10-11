// Package httpkit is the HTTP plumbing the admin API (internal/http) and the
// storefront API (internal/storefront) share: problem+json errors, strict JSON
// decoding, cursors, the client address, tracing.
//
// It exists so neither of those imports the other (P1-202, CLAUDE.md import
// rules). It never imports generated code: each API generates its own types
// from its own document, so the Problem written here is its own struct with the
// same JSON as the contracts' Problem.
package httpkit

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	apperrors "github.com/miqbalhamdani/new-commerce-api/internal/platform/errors"
)

// problem is the contracts' Problem (RFC 9457) as written on the wire.
type problem struct {
	Type     string           `json:"type"`
	Title    string           `json:"title"`
	Status   int              `json:"status"`
	Detail   *string          `json:"detail,omitempty"`
	Instance *string          `json:"instance,omitempty"`
	TraceID  string           `json:"trace_id"`
	Errors   []map[string]any `json:"errors,omitempty"`
}

// WriteError writes any error as an RFC 9457 application/problem+json response.
//
// Handlers pass whatever they have. Anything that is not a known error becomes
// an internal one, whose detail is fixed -- a database message or a file path
// reaching a response body is a disclosure, and the useful version of it goes
// to the log next to the trace id instead.
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	p := apperrors.From(err)
	traceID := apperrors.TraceID(r.Context())

	// Logged before the write, and with the same id the caller is about to be
	// given. That pairing is what makes the id worth quoting: the client gets a
	// string, the operator gets the cause.
	level := slog.LevelWarn
	if p.Status >= http.StatusInternalServerError {
		level = slog.LevelError
	}
	if traceID == "" {
		// No recording span, so the id a client is about to be handed resolves
		// to nothing. That is a missing telemetry.Setup, and it is silent from
		// the outside -- the response still looks well formed.
		slog.ErrorContext(r.Context(),
			"no trace id for an error response; telemetry was never started")
	}
	slog.LogAttrs(r.Context(), level, "request failed",
		slog.String("code", p.Code),
		slog.Int("status", p.Status),
		slog.String("method", r.Method),
		slog.String("path", r.URL.Path),
		slog.String("trace_id", traceID),
		slog.String("cause", causeOf(p)),
	)

	body := problem{
		Type:     "https://docs.example.com/errors/" + p.Code,
		Title:    p.Title,
		Status:   p.Status,
		Detail:   &p.Detail,
		Instance: &r.URL.Path,
		TraceID:  traceID,
	}
	for _, f := range p.Fields {
		e := map[string]any{"field": f.Name}
		if f.Detail != "" {
			e["detail"] = f.Detail
		}
		for k, v := range f.Extra {
			e[k] = v
		}
		body.Errors = append(body.Errors, e)
	}

	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(body)
}

func causeOf(err *apperrors.Error) string {
	if unwrapped := err.Unwrap(); unwrapped != nil {
		return unwrapped.Error()
	}
	return err.Detail
}

// maxBody caps a JSON request body. The largest legitimate one is a 500-row
// bulk upsert (P1-072); anything near this is a mistake or an attack.
const maxBody = 4 << 20

// serverManaged are the fields no client may send, on create or update alike
// (BR-008). Each route adds its own derived fields (a tenant's slug).
var serverManaged = []string{"id", "tenant_id", "version", "created_at", "updated_at", "archived_at", "path"}

// DecodeJSON decodes a request body into v, answering 422 itself on failure.
//
// In order: a server-managed field is validation_failed naming it (BR-008); a
// field v does not define is unknown_field naming it (04-api-spec.md §1.1);
// anything else that fails -- malformed JSON, a bad email, a timestamp with no
// offset (BR-007) -- is validation_failed.
func DecodeJSON(w http.ResponseWriter, r *http.Request, v any, alsoManaged ...string) bool {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		WriteError(w, r, apperrors.ValidationFailed("The request body could not be read.").WithCause(err))
		return false
	}

	var keys map[string]json.RawMessage
	if json.Unmarshal(raw, &keys) == nil {
		for _, k := range append(serverManaged, alsoManaged...) {
			if _, sent := keys[k]; sent {
				WriteError(w, r, apperrors.ValidationFailed(k+" is set by the server and cannot be sent.").
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
			WriteError(w, r, apperrors.UnknownField(strings.Trim(name, `"`)).WithCause(err))
			return false
		}
		WriteError(w, r, apperrors.ValidationFailed(
			"The request body is malformed or a field is not in the expected format.").WithCause(err))
		return false
	}
	return true
}

// Optional is a request field that tells "omitted" from "null" from a value
// (BR-009). On create, Null is a 422; on PATCH, omitted leaves the column
// alone and Null clears it where the ERD allows.
type Optional[T any] struct {
	Set   bool
	Null  bool
	Value T
}

func (o *Optional[T]) UnmarshalJSON(b []byte) error {
	o.Set = true
	if string(b) == "null" {
		o.Null = true
		return nil
	}
	// A fresh decoder would silently drop strictness: DisallowUnknownFields
	// does not survive into a custom UnmarshalJSON, so without this an
	// unknown field nested under an optional (a unit_price inside an order
	// line, BR-089) would be accepted.
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	return dec.Decode(&o.Value)
}

// RejectNull is BR-009 for fields that may not be null: on create any field,
// on PATCH the non-nullable ones.
func RejectNull(fields map[string]bool) error {
	for name, isNull := range fields {
		if isNull {
			return apperrors.ValidationFailed(name + " cannot be null; omit it instead.").
				WithFields(apperrors.Field{Name: name, Detail: "null not allowed"})
		}
	}
	return nil
}

// WriteJSON writes v with status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// EncodeCursor and DecodeCursor make an opaque page cursor from the last
// row's sort key. Opaque so clients cannot build one, and so the key can
// change without a contract change.
func EncodeCursor(key any) *string {
	b, _ := json.Marshal(key)
	s := base64.RawURLEncoding.EncodeToString(b)
	return &s
}

func DecodeCursor(c *string, key any) error {
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

// PageLimit is the requested page size, defaulting to 50 (04-api-spec.md §1)
// and clamped to the contract's 1-200. The clamp is load-bearing: the
// generated binder does not enforce the schema's bounds, and an unclamped
// limit=0 indexes out of range in every list that trims to the page size.
func PageLimit(l *int) int {
	switch {
	case l == nil:
		return 50
	case *l < 1:
		return 1
	case *l > 200:
		return 200
	}
	return *l
}

// BearerToken is the token in an Authorization: Bearer header.
func BearerToken(r *http.Request) (string, bool) {
	header := r.Header.Get("Authorization")
	// Case-insensitive: RFC 7235 makes the scheme name case-insensitive, and
	// clients do vary.
	if len(header) < 7 || !strings.EqualFold(header[:7], "bearer ") {
		return "", false
	}
	token := strings.TrimSpace(header[7:])
	return token, token != ""
}

// ClientIP is the peer address of the connection.
//
// ponytail: RemoteAddr only. Behind Caddy (P1-001) that is the proxy's address;
// once the proxy is fixed, trust its X-Forwarded-For hop and nothing further
// left -- trusting the header from anyone lets a client write its own IP into
// the audit log.
func ClientIP(r *http.Request) netip.Addr {
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}
	}
	return ap.Addr().Unmap()
}

// Tracing starts a span per request and names it after the matched route
// pattern rather than the URL, so /v1/products/{id} is one operation instead of
// one per product.
func Tracing(next http.Handler) http.Handler {
	return otelhttp.NewHandler(next, "",
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			if route := chi.RouteContext(r.Context()); route != nil && route.RoutePattern() != "" {
				return r.Method + " " + route.RoutePattern()
			}
			return r.Method + " " + r.URL.Path
		}),
	)
}
