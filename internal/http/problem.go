package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
)

// writeProblem writes an RFC 9457 application/problem+json response.
//
// P1-013 owns the real version of this: OpenTelemetry supplies the trace_id, so
// a support ticket quoting one goes straight to a span, and errors are built
// through internal/platform/errors rather than here. Until then a random id is
// still worth carrying -- it correlates a user's report with this server's logs,
// which is most of the value.
func writeProblem(w http.ResponseWriter, r *http.Request, status int, code, title, detail string) {
	body := Problem{
		Type:     "https://docs.example.com/errors/" + code,
		Title:    title,
		Status:   status,
		Detail:   &detail,
		Instance: ptr(r.URL.Path),
		TraceId:  traceID(r),
	}

	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// traceID returns the request's trace id, or a fresh one when there is no
// tracing yet.
func traceID(r *http.Request) string {
	if id := r.Header.Get("X-Trace-Id"); id != "" {
		return id
	}
	return uuid.NewString()
}

func ptr[T any](v T) *T { return &v }
