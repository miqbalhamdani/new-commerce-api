package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/platform/config"
)

// auditCase says how to exercise one mutating route as a signed-in owner, and
// which subject the single audit row it writes should name.
type auditCase struct {
	route   route
	request func(t *testing.T, s seeded) (req *http.Request, subjectType, subjectID string)
}

// auditCases grows one entry per mutating admin route, added by the item that
// adds the route (P1-021 brands first).
var auditCases []auditCase

// auditExempt are the mutating routes that write no audit row, each with why.
// Keep it short: an entry here is a write nobody can trace.
var auditExempt = map[route]string{
	{http.MethodPost, "/v1/auth/login"}:   "no actor yet; sign-in is not an admin write",
	{http.MethodPost, "/v1/auth/refresh"}: "token rotation, not an admin write",
	{http.MethodPost, "/v1/auth/logout"}:  "ends the caller's own session, not an admin write",
}

// TestEveryMutatingRouteIsAudited is P1-018's acceptance over the routes:
// every registered mutating admin route writes exactly one audit row in its
// own transaction (BR-018). The recorder's commit/rollback half is
// internal/db TestAudit.
func TestEveryMutatingRouteIsAudited(t *testing.T) {
	var mutating []route
	for _, r := range registeredRoutes(t) {
		if r.method != http.MethodGet && r.method != http.MethodHead && r.method != http.MethodOptions {
			mutating = append(mutating, r)
		}
	}

	t.Run("every mutating route has an audit case or a reason", func(t *testing.T) {
		for _, r := range mutating {
			_, exempt := auditExempt[r]
			covered := false
			for _, c := range auditCases {
				covered = covered || c.route == r
			}
			if !exempt && !covered {
				t.Errorf("%s writes no audit row -- add an auditCase in this file", r)
			}
		}
		t.Logf("%d mutating route(s), %d case(s), %d exempt", len(mutating), len(auditCases), len(auditExempt))
	})

	if len(auditCases) == 0 {
		return
	}

	ctx := t.Context()
	store := openAppStore(ctx, t)
	srv := newServer(t)
	owner, err := pgx.Connect(ctx, config.DatabaseURL())
	if err != nil {
		t.Fatalf("connect as owner: %v", err)
	}
	t.Cleanup(func() { _ = owner.Close(context.WithoutCancel(ctx)) })

	for _, c := range auditCases {
		t.Run(c.route.String(), func(t *testing.T) {
			tenantID := uuid.Must(uuid.NewV7())
			s := seedSignedInUserWithRole(ctx, t, store, tenantID, "owner")
			s.tenantID = tenantID.String()
			req, subjectType, subjectID := c.request(t, s)
			count := func(subjectID string) int {
				var n int
				if err := owner.QueryRow(ctx, `SELECT count(*) FROM audit_log
					WHERE tenant_id = $1 AND subject_type = $2 AND subject_id = $3`,
					tenantID, subjectType, subjectID).Scan(&n); err != nil {
					t.Fatalf("count audit rows: %v", err)
				}
				return n
			}
			// A case may set its subject up through the API first, which
			// audits too; only the rows this request adds count.
			before := count(subjectID)

			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)
			if rec.Code >= 300 {
				t.Fatalf("status %d: %s", rec.Code, rec.Body)
			}
			if subjectID == "" { // a create: the subject is in the response
				var created struct {
					ID string `json:"id"`
				}
				_ = json.Unmarshal(rec.Body.Bytes(), &created)
				subjectID = created.ID
			}

			if n := count(subjectID) - before; n != 1 {
				t.Errorf("%d audit rows, want exactly 1", n)
			}
		})
	}
}
