package httpapi_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

func init() {
	isolationCases = append(isolationCases, isolationCase{
		route: route{"GET", "/v1/jobs/{id}"}, seed: seedJob,
		request: func(t *testing.T, s seeded) *http.Request {
			return bearerRequest(t, http.MethodGet, "/v1/jobs/"+s.otherID, s.accessToken)
		}})
}

// seedJob is a signed-in admin plus a finished import job; the job id is the
// marker, since a leaked job would carry it.
func seedJob(ctx context.Context, t *testing.T, store *db.Store, tenantID uuid.UUID) seeded {
	t.Helper()
	s := seedSignedInUserWithRole(ctx, t, store, tenantID, auth.RoleAdmin)
	s.id = insertJob(ctx, t, store, tenantID, "product_import")
	// The marker lives in the result, not the id: a 404's instance echoes the
	// requested path, which holds the other tenant's id by design.
	s.marker = "created-" + s.id
	return s
}

func insertJob(ctx context.Context, t *testing.T, store *db.Store, tenantID uuid.UUID, kind string) string {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	if err := store.InTenantTx(tenant.NewContext(ctx, tenantID), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO jobs (id, tenant_id, kind, state, processed, total, result, finished_at)
			VALUES ($1, $2, $3, 'done', 10, 10, jsonb_build_object('created', 10, 'note', $4::text), now())`,
			id, tenantID, kind, "created-"+id.String())
		return err
	}); err != nil {
		t.Fatalf("seed job: %v", err)
	}
	t.Cleanup(func() {
		bg := tenant.NewContext(context.WithoutCancel(ctx), tenantID)
		_ = store.InTenantTx(bg, func(tx pgx.Tx) error {
			_, err := tx.Exec(bg, `DELETE FROM jobs WHERE id = $1`, id)
			return err
		})
	})
	return id.String()
}

// TestGetJob: the job's own kind decides who may read it (04-api-spec.md §9).
func TestGetJob(t *testing.T) {
	ctx := t.Context()
	store := openAppStore(ctx, t)
	tenantID := uuid.Must(uuid.NewV7())
	admin := seedSignedInUserWithRole(ctx, t, store, tenantID, auth.RoleAdmin)
	importJob := insertJob(ctx, t, store, tenantID, "product_import")

	code, j := apiClient(t, admin)("GET", "/v1/jobs/"+importJob, nil)
	if code != 200 || j["state"] != "done" || j["processed"] != float64(10) || j["error"] != nil ||
		j["result"].(map[string]any)["created"] != float64(10) {
		t.Fatalf("admin: %d %v", code, j)
	}

	v := signInAnotherUser(ctx, t, store, tenantID, auth.RoleViewer)
	code, p := apiClient(t, v)("GET", "/v1/jobs/"+importJob, nil)
	assertProblem(t, code, p, 403, "permission_denied", "")

	media := insertJob(ctx, t, store, tenantID, "image_derivatives")
	if code, p := apiClient(t, v)("GET", "/v1/jobs/"+media, nil); code != 200 {
		t.Errorf("viewer reading an image job: %d %v", code, p)
	}
}
