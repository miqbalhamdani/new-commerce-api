package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestTimestampWithoutOffsetIs422: a client timestamp must carry an offset;
// one without is 422, and any offset is the same instant (BR-007).
func TestTimestampWithoutOffsetIs422(t *testing.T) {
	for _, tt := range []struct {
		body string
		ok   bool
	}{
		{`{"at":"2026-10-06T16:15:00"}`, false},
		{`{"at":"2026-10-06"}`, false},
		{`{"at":"2026-10-06T16:15:00+07:00"}`, true},
		{`{"at":"2026-10-06T09:15:00Z"}`, true},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/x", strings.NewReader(tt.body))
		var v struct{ At time.Time }
		ok := decodeJSON(rec, req, &v)
		if ok != tt.ok {
			t.Errorf("%s: decoded %v, want %v", tt.body, ok, tt.ok)
		}
		if !ok && rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: status %d, want 422", tt.body, rec.Code)
		}
		if ok && !v.At.Equal(time.Date(2026, 10, 6, 9, 15, 0, 0, time.UTC)) {
			t.Errorf("%s: decoded %s, want the same instant", tt.body, v.At)
		}
	}
}

// TestUnknownFieldIs422: a field the endpoint does not define is
// 422 unknown_field naming it, never silently dropped (04-api-spec.md 1.1).
func TestUnknownFieldIs422(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/login",
		strings.NewReader(`{"email":"budi@example.com","password":"long-enough","remember_me":true}`))
	var v LoginRequest
	if decodeJSON(rec, req, &v) {
		t.Fatal("decoded a body with an unknown field")
	}
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("status %d, want 422", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "/errors/unknown_field") || !strings.Contains(body, `"field":"remember_me"`) {
		t.Errorf("body does not name unknown_field and remember_me: %s", body)
	}
}
