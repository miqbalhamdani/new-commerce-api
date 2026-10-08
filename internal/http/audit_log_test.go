package httpapi_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/db"
)

func init() {
	isolationCases = append(isolationCases, isolationCase{route: route{"GET", "/v1/audit-log"}, seed: seedAudited,
		request: func(t *testing.T, s seeded) *http.Request {
			return bearerRequest(t, http.MethodGet, "/v1/audit-log?limit=200", s.accessToken)
		}})
}

// seedAudited is a signed-in owner who has created a brand through the API,
// so the audit log holds a row whose after carries the marker.
func seedAudited(ctx context.Context, t *testing.T, store *db.Store, tenantID uuid.UUID) seeded {
	t.Helper()
	s := seedSignedInUserWithRole(ctx, t, store, tenantID, auth.RoleOwner)
	s.marker = "Audited " + uuid.NewString()
	createBrand(t, s, s.marker)
	return s
}

// TestAuditLog is P1-077's acceptance: newest first, filterable by subject
// and actor, with actor, action and before/after per row (BR-018).
func TestAuditLog(t *testing.T) {
	ctx := t.Context()
	store := openAppStore(ctx, t)
	tenantID := uuid.Must(uuid.NewV7())
	owner := seedSignedInUserWithRole(ctx, t, store, tenantID, auth.RoleOwner)
	do := apiClient(t, owner)

	brand := createBrand(t, owner, "Before")
	if code, _ := do("PATCH", "/v1/brands/"+brand, map[string]any{"name": "After"}); code != 200 {
		t.Fatal("rename")
	}
	createBrand(t, owner, "Other")

	code, p := do("GET", "/v1/audit-log?subject_type=brand&subject_id="+brand, nil)
	if code != 200 {
		t.Fatalf("%d %v", code, p)
	}
	rows := p["data"].([]any)
	if len(rows) != 2 {
		t.Fatalf("rows %v", rows)
	}
	latest := rows[0].(map[string]any)
	if latest["action"] != "brand.update" || latest["before"].(map[string]any)["name"] != "Before" ||
		latest["after"].(map[string]any)["name"] != "After" || latest["actor"].(map[string]any)["name"] != "Isolation user" {
		t.Errorf("newest row %v", latest)
	}
	if _, hasID := latest["id"]; hasID {
		t.Error("an audit entry exposes its internal id")
	}

	t.Run("pages newest first without repeats", func(t *testing.T) {
		var seen []string
		path := "/v1/audit-log?limit=1"
		for i := 0; path != "" && i < 10; i++ {
			_, p := do("GET", path, nil)
			for _, r := range p["data"].([]any) {
				seen = append(seen, r.(map[string]any)["created_at"].(string))
			}
			path = ""
			if c, ok := p["next_cursor"].(string); ok {
				path = "/v1/audit-log?limit=1&cursor=" + c
			}
		}
		if len(seen) != 3 || seen[0] < seen[2] {
			t.Errorf("order %v", seen)
		}
	})
	t.Run("date filters are midnight WIB; an offset-less time is 422", func(t *testing.T) {
		tomorrow := time.Now().Add(24 * time.Hour).Format(time.DateOnly)
		_, p := do("GET", "/v1/audit-log?from="+tomorrow, nil)
		if len(p["data"].([]any)) != 0 {
			t.Errorf("from tomorrow: %v", p)
		}
		code, p := do("GET", "/v1/audit-log?from=2026-10-06T10:00:00", nil)
		assertProblem(t, code, p, 422, "validation_failed", "from")
		code, p = do("GET", "/v1/audit-log?actor_id="+uuid.NewString(), nil)
		if code != 200 || len(p["data"].([]any)) != 0 {
			t.Errorf("unknown actor: %d %v", code, p)
		}
	})
	t.Run("ops cannot read it", func(t *testing.T) {
		ops := signInAnotherUser(ctx, t, store, tenantID, auth.RoleOps)
		code, p := apiClient(t, ops)("GET", "/v1/audit-log", nil)
		assertProblem(t, code, p, 403, "permission_denied", "")
		if !strings.Contains(p["detail"].(string), "audit_log:read") {
			t.Errorf("detail %v", p["detail"])
		}
	})
}
