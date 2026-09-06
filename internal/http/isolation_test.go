// Tenant isolation suite (P1-008).
//
// Two things live here. The registry below lists, per route, how to reach one
// tenant's row as another tenant -- and the coverage check walks the routes the
// contract actually registers and fails when one has no entry. A new route
// without an isolation case is an incomplete route, and this is what says so
// rather than a reviewer having to notice.
//
// Right now the contract has no paths, so both lists are empty and the coverage
// check passes over nothing. TestIsolationHarness is what keeps that honest: it
// builds a route that leaks and proves the harness catches it, and a route that
// does not and proves it does not cry wolf.
//
// Two things swap when their items land. Auth is a tenant on the request
// context here; from P1-011 it is a real token, and withTenant becomes the auth
// middleware. And routes are walked from a router this file builds, because
// cmd/api does not mount the generated one yet; when it does, walk that.
package httpapi_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	httpapi "github.com/miqbalhamdani/new-commerce-api/internal/http"

	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/platform/config"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

// route is one method-and-pattern pair registered on the router.
type route struct {
	method  string
	pattern string
}

func (r route) String() string { return r.method + " " + r.pattern }

// isolationCase says how to exercise one route as tenant A after tenant B owns
// a row, so the suite can check that B's row does not come back.
//
// seed runs as the given tenant and returns a marker -- any string that would
// appear in a response that leaked that tenant's data, usually a row id.
type isolationCase struct {
	route   route
	seed    func(ctx context.Context, t *testing.T, store *db.Store, tenantID uuid.UUID) string
	request func(t *testing.T, tenantID uuid.UUID) *http.Request
}

// isolationCases grows one entry per route, added by the item that adds the
// route. P1-011 auth, P1-021 brands, P1-024 categories, P1-028 products.
var isolationCases []isolationCase

// TestTenantIsolation is P1-008's acceptance: two seeded tenants, and tenant A
// sees zero of tenant B's rows on every registered route.
func TestTenantIsolation(t *testing.T) {
	registered := registeredRoutes(t)

	// The guard that makes the rest of this file self-maintaining.
	t.Run("every registered route has an isolation case", func(t *testing.T) {
		missing := routesWithoutCase(registered, isolationCases)
		for _, r := range missing {
			t.Errorf("%s has no isolation case -- add one to isolationCases in this file", r)
		}
		t.Logf("%d registered route(s), %d case(s)", len(registered), len(isolationCases))
	})

	if len(isolationCases) == 0 {
		// Not a skip of the acceptance: TestIsolationHarness below proves the
		// machinery works on a route built for the purpose.
		t.Log("no routes in the contract yet; TestIsolationHarness covers the machinery")
		return
	}

	if newServer == nil {
		t.Fatal("isolationCases is not empty but newServer is nil -- " +
			"point it at the server cmd/api serves")
	}

	ctx := t.Context()
	store := openAppStore(ctx, t)
	srv := newServer(t)

	for _, c := range isolationCases {
		t.Run(c.route.String(), func(t *testing.T) {
			tenantA, tenantB := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
			c.seed(ctx, t, store, tenantA)
			markerB := c.seed(ctx, t, store, tenantB)

			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, c.request(t, tenantA))

			assertNoLeak(t, rec, markerB)
		})
	}
}

// TestIsolationHarness proves the suite above can fail. Without it, an empty
// case list and an empty route list would make TestTenantIsolation a test that
// cannot report anything.
func TestIsolationHarness(t *testing.T) {
	ctx := t.Context()
	store := openAppStore(ctx, t)

	owner, err := pgx.Connect(ctx, config.DatabaseURL())
	if err != nil {
		t.Fatalf("connect as owner: %v", err)
	}
	t.Cleanup(func() { _ = owner.Close(ctx) })

	table := createTenantTable(ctx, t, owner, "iso_scratch")
	tenantA, tenantB := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	seedRow(ctx, t, store, table, tenantA)
	markerB := seedRow(ctx, t, store, table, tenantB)

	t.Run("catches a route that leaks", func(t *testing.T) {
		// The realistic bug this models: a handler reaching for the owning
		// connection instead of the application's. The owner is a superuser
		// locally, superusers bypass row level security outright, and so the
		// tenant filter silently does nothing.
		leaky := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			notes, err := allNotes(r.Context(), owner, table)
			if err != nil {
				t.Errorf("leaky handler: %v", err)
				return
			}
			_, _ = fmt.Fprint(w, strings.Join(notes, ","))
		})

		rec := httptest.NewRecorder()
		leaky.ServeHTTP(rec, requestAsTenant(t, tenantA))

		// Deliberately inverted: the harness is supposed to fail here.
		if !strings.Contains(rec.Body.String(), markerB) {
			t.Fatalf("the leaky handler did not leak, so this proves nothing; body=%q", rec.Body)
		}
		if leaked := findLeak(rec, markerB); leaked == "" {
			t.Error("assertNoLeak would have passed a response containing tenant B's row")
		}
	})

	t.Run("passes a route that isolates", func(t *testing.T) {
		clean := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var notes []string
			err := store.InTenantTx(r.Context(), func(tx pgx.Tx) error {
				var err error
				notes, err = allNotesTx(r.Context(), tx, table)
				return err
			})
			if err != nil {
				t.Errorf("clean handler: %v", err)
				return
			}
			_, _ = fmt.Fprint(w, strings.Join(notes, ","))
		})

		rec := httptest.NewRecorder()
		clean.ServeHTTP(rec, requestAsTenant(t, tenantA))

		assertNoLeak(t, rec, markerB)
		if rec.Body.Len() == 0 {
			t.Error("tenant A saw nothing at all -- that is fail-shut, not isolation")
		}
	})

	// registeredRoutes finding nothing today is only meaningful if it would
	// find something when there is something. An empty result and a broken walk
	// look identical otherwise.
	t.Run("the route walk finds registered routes", func(t *testing.T) {
		r := chi.NewRouter()
		r.Get("/things", func(http.ResponseWriter, *http.Request) {})
		r.Patch("/things/{id}", func(http.ResponseWriter, *http.Request) {})

		got := walkRoutes(t, r)
		want := []route{
			{method: "GET", pattern: "/things"},
			{method: "PATCH", pattern: "/things/{id}"},
		}
		if !slices.Equal(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("coverage guard reports an uncovered route", func(t *testing.T) {
		covered := route{method: "GET", pattern: "/covered"}
		uncovered := route{method: "GET", pattern: "/uncovered"}

		missing := routesWithoutCase(
			[]route{covered, uncovered},
			[]isolationCase{{route: covered}},
		)
		if len(missing) != 1 || missing[0] != uncovered {
			t.Errorf("got %v, want exactly %v", missing, uncovered)
		}
	})
}

// --- harness ---------------------------------------------------------------

// registeredRoutes lists what the generated code actually registers, so the
// coverage check cannot drift from the contract.
//
// It walks a router built here rather than cmd/api's, because cmd/api still
// serves a plain mux -- there is nothing in the contract to route yet. Point
// this at the real server the moment there is.
func registeredRoutes(t *testing.T) []route {
	t.Helper()

	r := chi.NewRouter()
	httpapi.HandlerFromMux(stubServer{}, r)
	return walkRoutes(t, r)
}

// walkRoutes lists every method and pattern registered on a router.
func walkRoutes(t *testing.T, r chi.Router) []route {
	t.Helper()

	var routes []route
	err := chi.Walk(r, func(method, pattern string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		routes = append(routes, route{method: method, pattern: pattern})
		return nil
	})
	if err != nil {
		t.Fatalf("walk routes: %v", err)
	}
	return routes
}

// stubServer satisfies the generated interface so a router can be built from
// it. The interface is empty until P1-011; once it is not, this stops compiling
// and should be replaced by the real handler rather than grown method by method.
type stubServer struct{}

// routesWithoutCase returns the registered routes that no case covers.
func routesWithoutCase(registered []route, cases []isolationCase) []route {
	var missing []route
	for _, r := range registered {
		if !slices.ContainsFunc(cases, func(c isolationCase) bool { return c.route == r }) {
			missing = append(missing, r)
		}
	}
	return missing
}

// assertNoLeak fails when the response carries another tenant's marker.
func assertNoLeak(t *testing.T, rec *httptest.ResponseRecorder, marker string) {
	t.Helper()
	if where := findLeak(rec, marker); where != "" {
		t.Errorf("tenant B's row leaked in the response %s\nmarker: %s\nbody: %s",
			where, marker, rec.Body.String())
	}
}

// findLeak looks for the marker anywhere in the response -- body or headers --
// and names where it was found. Searching the raw bytes rather than a parsed
// shape means one check works for every route regardless of payload.
func findLeak(rec *httptest.ResponseRecorder, marker string) string {
	if strings.Contains(rec.Body.String(), marker) {
		return "body"
	}
	for name, values := range rec.Header() {
		for _, v := range values {
			if strings.Contains(v, marker) {
				return "header " + name
			}
		}
	}
	return ""
}

// requestAsTenant builds a request carrying a tenant, standing in for the auth
// middleware until P1-011 issues real tokens.
func requestAsTenant(t *testing.T, id uuid.UUID) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	return req.WithContext(tenant.NewContext(req.Context(), id))
}

// newServer builds the server the cases run against.
//
// Nil until there is one: cmd/api serves a plain mux with only /healthz on it,
// and the generated router has nothing to mount. P1-011 sets this to the real
// server. A non-empty case list with this still nil is a mistake, and the suite
// says so rather than quietly testing a router built for the test.
var newServer func(t *testing.T) http.Handler

// --- fixtures --------------------------------------------------------------

func openAppStore(ctx context.Context, t *testing.T) *db.Store {
	t.Helper()
	store, err := db.New(ctx, config.AppDatabaseURL())
	if err != nil {
		t.Fatalf("connect as app_user: %v\n\nIs PostgreSQL running and migrated?\n"+
			"  brew services start postgresql@18\n  make db-create && make migrate", err)
	}
	t.Cleanup(store.Close)
	return store
}

func createTenantTable(ctx context.Context, t *testing.T, owner *pgx.Conn, name string) string {
	t.Helper()

	drop := fmt.Sprintf(`DROP TABLE IF EXISTS %s`, name)
	if _, err := owner.Exec(ctx, drop); err != nil {
		t.Fatalf("drop stale %s: %v", name, err)
	}
	if _, err := owner.Exec(ctx, fmt.Sprintf(
		`CREATE TABLE %s (id uuid PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL, note text)`,
		name)); err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	if _, err := owner.Exec(ctx, `SELECT enable_tenant_rls($1)`, name); err != nil {
		t.Fatalf("enable_tenant_rls(%s): %v", name, err)
	}
	t.Cleanup(func() {
		if _, err := owner.Exec(context.WithoutCancel(ctx), drop); err != nil {
			t.Errorf("drop %s: %v", name, err)
		}
	})
	return name
}

// seedRow inserts one row for a tenant and returns a marker unique to it.
func seedRow(ctx context.Context, t *testing.T, store *db.Store, table string, tenantID uuid.UUID) string {
	t.Helper()

	marker := "marker-" + uuid.Must(uuid.NewV7()).String()
	err := store.InTenantTx(tenant.NewContext(ctx, tenantID), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			fmt.Sprintf(`INSERT INTO %s (tenant_id, note) VALUES ($1, $2)`, table),
			tenantID, marker)
		return err
	})
	if err != nil {
		t.Fatalf("seed row for %s: %v", tenantID, err)
	}
	return marker
}

func allNotes(ctx context.Context, conn *pgx.Conn, table string) ([]string, error) {
	rows, err := conn.Query(ctx, fmt.Sprintf(`SELECT note FROM %s`, table))
	if err != nil {
		return nil, err
	}
	return collectNotes(rows)
}

func allNotesTx(ctx context.Context, tx pgx.Tx, table string) ([]string, error) {
	rows, err := tx.Query(ctx, fmt.Sprintf(`SELECT note FROM %s`, table))
	if err != nil {
		return nil, err
	}
	return collectNotes(rows)
}

func collectNotes(rows pgx.Rows) ([]string, error) {
	defer rows.Close()
	var notes []string
	for rows.Next() {
		var note string
		if err := rows.Scan(&note); err != nil {
			return nil, err
		}
		notes = append(notes, note)
	}
	return notes, rows.Err()
}
