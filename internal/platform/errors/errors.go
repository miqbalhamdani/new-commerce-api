// Package errors is the error taxonomy every handler answers with.
//
// It defines what an error *is* -- a code, a status, and something a person can
// read -- and deliberately not how it is written to the wire. The wire shape is
// RFC 9457 and lives in internal/http, which owns the generated Problem type;
// nothing imports internal/http, so the taxonomy cannot live there.
package errors

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"go.opentelemetry.io/otel/trace"
)

// The canonical codes for this phase (04-api-spec.md §1.1). The code is also the
// last segment of the type URI a client receives.
const (
	CodeValidationFailed = "validation_failed"
	CodeUnknownField     = "unknown_field"
	CodeVersionConflict  = "version_conflict"
	CodeDuplicateSKU     = "duplicate_sku"
	CodeCategoryInUse    = "category_in_use"
	CodePermissionDenied = "permission_denied"
	CodeNotFound         = "not_found"
	CodeRateLimited      = "rate_limited"
	CodeUnauthenticated  = "unauthenticated"
	CodeInternal         = "internal"
)

// Field is one entry in a Problem's errors array: which field, and what about
// it. Extra carries whatever the specific failure needs -- a version conflict
// reports expected and supplied.
type Field struct {
	Name   string
	Detail string
	Extra  map[string]any
}

// Error is a failure a client is allowed to see.
//
// Anything that is not one of these is an internal error: the detail on those
// is fixed and uninformative on purpose, because the alternative is leaking a
// database message or a file path into a response.
type Error struct {
	Code   string
	Status int
	Title  string
	Detail string
	Fields []Field

	// cause is logged, never sent. It is what makes a trace_id worth quoting:
	// the id reaches a support ticket, this reaches the operator.
	cause error
}

func (e *Error) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Detail, e.cause)
	}
	return e.Code + ": " + e.Detail
}

func (e *Error) Unwrap() error { return e.cause }

// WithCause attaches the underlying failure for logging. It never reaches the
// client.
func (e *Error) WithCause(err error) *Error {
	e.cause = err
	return e
}

// WithFields attaches field-level detail.
func (e *Error) WithFields(fields ...Field) *Error {
	e.Fields = append(e.Fields, fields...)
	return e
}

func Unauthenticated(detail string) *Error {
	return &Error{Code: CodeUnauthenticated, Status: http.StatusUnauthorized,
		Title: "Unauthenticated", Detail: detail}
}

// PermissionDenied names the permission that was required.
//
// That naming is an acceptance criterion, not a nicety (BR-024): it is the
// difference between "you cannot do this" and "ask your owner for users:write".
func PermissionDenied(permission string) *Error {
	return &Error{Code: CodePermissionDenied, Status: http.StatusForbidden,
		Title:  "Permission denied",
		Detail: "This action requires the " + permission + " permission."}
}

func NotFound(detail string) *Error {
	return &Error{Code: CodeNotFound, Status: http.StatusNotFound,
		Title: "Not found", Detail: detail}
}

// RateLimited is the 429 for a caller over its BR-014 limit. The middleware
// that raises it sets Retry-After and the RateLimit-* headers.
func RateLimited() *Error {
	return &Error{Code: CodeRateLimited, Status: http.StatusTooManyRequests,
		Title: "Rate limited", Detail: "Too many requests. Wait and try again."}
}

// UnknownField is the 422 for a request field the endpoint does not define
// (04-api-spec.md 1.1). Never silently ignored: a client sending "price" to a
// cart would otherwise believe it had set one (BR-089).
func UnknownField(name string) *Error {
	return (&Error{Code: CodeUnknownField, Status: http.StatusUnprocessableEntity,
		Title: "Unknown field", Detail: "The request has a field this endpoint does not accept: " + name + "."}).
		WithFields(Field{Name: name, Detail: "not a field of this request"})
}

// CategoryInUse is the 409 for archiving a category that still has live
// children or products in its subtree, with both counts (BR-036).
func CategoryInUse(children, products int32) *Error {
	return (&Error{Code: CodeCategoryInUse, Status: http.StatusConflict,
		Title: "Category in use", Detail: "Move or archive its subcategories and products first."}).
		WithFields(Field{Name: "children", Extra: map[string]any{"count": children}},
			Field{Name: "products", Extra: map[string]any{"count": products}})
}

// DuplicateSKU is the 409 for a SKU another variant of this tenant holds,
// naming the product that holds it when known (BR-039).
func DuplicateSKU(sku, holder string) *Error {
	detail := "This SKU is already used by another product."
	if holder != "" {
		detail = "SKU " + sku + " is used by " + holder + "."
	}
	return (&Error{Code: CodeDuplicateSKU, Status: http.StatusConflict, Title: "Duplicate SKU", Detail: detail}).
		WithFields(Field{Name: "sku", Detail: detail})
}

func ValidationFailed(detail string) *Error {
	return &Error{Code: CodeValidationFailed, Status: http.StatusUnprocessableEntity,
		Title: "Validation failed", Detail: detail}
}

func VersionConflict(expected, supplied int) *Error {
	return (&Error{Code: CodeVersionConflict, Status: http.StatusConflict,
		Title:  "Version conflict",
		Detail: "This was changed by someone else. Reload and try again."}).
		WithFields(Field{Name: "version", Extra: map[string]any{
			"expected": expected, "supplied": supplied}})
}

// Internal wraps anything the client has no business seeing.
//
// The detail is fixed. A database error, a connection string or a file path
// reaching a response body is a disclosure, and the useful version of that
// information goes to the log alongside the trace id instead.
func Internal(cause error) *Error {
	return &Error{Code: CodeInternal, Status: http.StatusInternalServerError,
		Title: "Internal error", Detail: "Something went wrong on our side.",
		cause: cause}
}

// From turns any error into one a client may see, so a handler can pass
// whatever it has and still not leak.
func From(err error) *Error {
	var known *Error
	if errors.As(err, &known) {
		return known
	}
	return Internal(err)
}

// TraceID returns the OpenTelemetry trace id of the current span.
//
// This is the whole of "resolvable to a span": the id in a response is the id
// of the span that produced it, so a support ticket quoting one lands on the
// request that failed. It is empty only when there is no recording span, which
// means telemetry was never started -- possible in a unit test, and a
// misconfiguration anywhere else.
func TraceID(ctx context.Context) string {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.HasTraceID() {
		return ""
	}
	return sc.TraceID().String()
}
