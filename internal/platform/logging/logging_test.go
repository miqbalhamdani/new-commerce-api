package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

// TestRedaction is P1-016's acceptance: anything off the allow list is
// redacted, and credential headers and cookies never appear at all (BR-013).
func TestRedaction(t *testing.T) {
	const secret = "s3cret-value"
	var buf bytes.Buffer
	log := New(&buf)

	log.Info("request failed",
		"status", 401, "path", "/v1/roles",
		"email", "budi@example.com",
		"Authorization", "Bearer "+secret,
		"X-Api-Key", secret, "X-Order-Token", secret,
		"Cookie", "refresh_token="+secret, "password", secret,
		slog.Group("req", "authorization", secret, "path", "/v1/roles"),
	)

	out := buf.String()
	for _, leak := range []string{secret, "budi@example.com"} {
		if strings.Contains(out, leak) {
			t.Errorf("log line contains %q: %s", leak, out)
		}
	}
	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("not JSON: %v: %s", err, out)
	}
	if line["status"] != float64(401) || line["path"] != "/v1/roles" || line["msg"] != "request failed" {
		t.Errorf("allowed fields were altered: %s", out)
	}
	if line["email"] != Redacted {
		t.Errorf("email = %v, want %s", line["email"], Redacted)
	}
}

// TestDeniedBeatsAllowed: allowing a credential key by mistake still logs
// nothing.
func TestDeniedBeatsAllowed(t *testing.T) {
	allowed["authorization"] = true
	t.Cleanup(func() { delete(allowed, "authorization") })

	var buf bytes.Buffer
	New(&buf).Info("x", "authorization", "Bearer abc")
	if strings.Contains(buf.String(), "abc") {
		t.Errorf("denied key logged: %s", buf.String())
	}
}
