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
