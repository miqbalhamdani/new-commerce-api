package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/miqbalhamdani/new-commerce-api/internal/platform/config"
)

// TestLimitedRoleMode is BR-001's limited-roles mode, proven against a
// database migrated in it: CI's limited-roles job, as a role with neither
// CREATEROLE nor BYPASSRLS, like the managed database it stands in for. In the
// default mode it has nothing to check; TestBootstrapMigration covers that.
func TestLimitedRoleMode(t *testing.T) {
	ctx := t.Context()
	if err := run(0); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	// The app's DSN, not the owner's: in limited mode they are the same role,
	// but the tests' owner DSN may carry app.rls_bypass to read as an oracle.
	conn, err := pgx.Connect(ctx, config.AppDatabaseURL())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(ctx) })

	var mode string
	if err := conn.QueryRow(ctx, `SELECT db_role_mode()`).Scan(&mode); err != nil {
		t.Fatalf("db_role_mode(): %v", err)
	}
	if mode != "limited" {
		t.Skipf("database is in %s mode", mode)
	}

	t.Run("the app is the schema owner, and not one RLS ignores", func(t *testing.T) {
		var super, bypass bool
		if err := conn.QueryRow(ctx, `SELECT rolsuper, rolbypassrls FROM pg_roles WHERE rolname = current_user`).
			Scan(&super, &bypass); err != nil {
			t.Fatal(err)
		}
		if super || bypass {
			t.Fatalf("superuser=%v bypassrls=%v -- FORCE would not bind the app", super, bypass)
		}
	})

	t.Run("every tenant table has tenant_isolation and a SELECT-only limited_read_bypass", func(t *testing.T) {
		rows, err := conn.Query(ctx, `
			SELECT c.relname,
			       count(*) FILTER (WHERE p.polname = 'tenant_isolation'),
			       count(*) FILTER (WHERE p.polname = 'limited_read_bypass' AND p.polcmd = 'r')
			  FROM pg_class c
			  JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = 'public'
			  LEFT JOIN pg_policy p ON p.polrelid = c.oid
			 WHERE c.relkind = 'r'
			   AND EXISTS (SELECT 1 FROM pg_attribute a WHERE a.attrelid = c.oid
			               AND a.attname = 'tenant_id' AND NOT a.attisdropped)
			 GROUP BY c.relname`)
		if err != nil {
			t.Fatal(err)
		}
		tables, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (string, error) {
			var name string
			var isolation, bypass int
			if err := r.Scan(&name, &isolation, &bypass); err != nil {
				return "", err
			}
			if isolation != 1 || bypass != 1 {
				t.Errorf("%s: tenant_isolation=%d limited_read_bypass(SELECT)=%d, want 1 and 1", name, isolation, bypass)
			}
			return name, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(tables) == 0 {
			t.Fatal("no tenant tables found")
		}
	})

	t.Run("the four lookups belong to the owner and nobody else may call them", func(t *testing.T) {
		for _, fn := range []string{"auth_lookup_user(citext)", "auth_lookup_refresh_token(text)",
			"resolve_api_key(text)", "resolve_midtrans_webhook(uuid)"} {
			var definer, mine, public bool
			if err := conn.QueryRow(ctx, `
				SELECT p.prosecdef, p.proowner = (SELECT oid FROM pg_roles WHERE rolname = current_user),
				       has_function_privilege('public', p.oid, 'EXECUTE')
				  FROM pg_proc p WHERE p.oid = $1::regprocedure`, fn).Scan(&definer, &mine, &public); err != nil {
				t.Fatalf("%s: %v", fn, err)
			}
			if !definer || !mine || public {
				t.Errorf("%s: security definer=%v owned by the app=%v PUBLIC may execute=%v", fn, definer, mine, public)
			}
		}
	})

	// Permissive policies are OR'd. Unless the planner folds the bypass away,
	// `tenant_id = X OR bypass` cannot use a tenant_id index anywhere.
	t.Run("the app's plans carry no bypass, so tenant indexes serve", func(t *testing.T) {
		tx, err := conn.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true)`, uuid.Must(uuid.NewV7()).String()); err != nil {
			t.Fatal(err)
		}
		rows, err := tx.Query(ctx, `EXPLAIN SELECT id FROM products ORDER BY created_at DESC LIMIT 50`)
		if err != nil {
			t.Fatal(err)
		}
		plan, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range plan {
			if strings.Contains(line, "rls_bypass") {
				t.Fatalf("the bypass reached the app's plan:\n%s", strings.Join(plan, "\n"))
			}
		}
	})

	// A pooled connection keeps app.tenant_id as '' once a tenant transaction
	// has ended, and tenant_isolation casts it to uuid next to the bypass: the
	// lookups must not trip on that, and must hand the setting back unchanged.
	t.Run("a lookup works on a connection that has served a tenant", func(t *testing.T) {
		tx, err := conn.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true)`, uuid.Must(uuid.NewV7()).String()); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		var tenant string
		if err := conn.QueryRow(ctx, `SELECT coalesce(current_setting('app.tenant_id', true), '<unset>')`).Scan(&tenant); err != nil {
			t.Fatal(err)
		}
		if tenant != "" {
			t.Fatalf("app.tenant_id after a tenant transaction is %q; this test needs it to be ''", tenant)
		}
		for _, q := range []string{
			`SELECT count(*) FROM auth_lookup_user('nobody@example.com')`,
			`SELECT count(*) FROM auth_lookup_refresh_token('nothing')`,
			`SELECT count(*) FROM resolve_api_key('nothing')`,
			`SELECT count(*) FROM resolve_midtrans_webhook('00000000-0000-0000-0000-000000000001')`,
		} {
			var n int
			if err := conn.QueryRow(ctx, q).Scan(&n); err != nil {
				t.Errorf("%s: %v", q, err)
			}
		}
		if err := conn.QueryRow(ctx, `SELECT coalesce(current_setting('app.tenant_id', true), '<unset>')`).Scan(&tenant); err != nil {
			t.Fatal(err)
		}
		if tenant != "" {
			t.Errorf("app.tenant_id after the lookups is %q, want it handed back as ''", tenant)
		}
	})

	// The bypass is the lookups' and the backup's alone: a write with it set
	// and no tenant is refused, and it is off again once a lookup returns.
	t.Run("the bypass admits reads only, and a lookup leaves it off", func(t *testing.T) {
		// A connection of its own: on one that has served a tenant, the
		// refusal is tenant_isolation's ''::uuid rather than RLS itself.
		conn, err := pgx.Connect(ctx, config.AppDatabaseURL())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close(ctx) }()
		tx, err := conn.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, `SELECT set_config('app.rls_bypass', 'on', true)`); err != nil {
			t.Fatal(err)
		}
		_, err = tx.Exec(ctx, `INSERT INTO tenants (id, name, slug, order_prefix) VALUES ($1, 'x', $2, 'LIM')`,
			uuid.Must(uuid.NewV7()), "limited-"+uuid.NewString()[:8])
		if err != nil {
			t.Fatalf("tenants has no RLS, so this insert should pass: %v", err)
		}
		_, err = tx.Exec(ctx, `SAVEPOINT s`)
		if err != nil {
			t.Fatal(err)
		}
		_, err = tx.Exec(ctx, `INSERT INTO brands (id, tenant_id, name, slug)
			SELECT $1, id, 'x', 'x' FROM tenants LIMIT 1`, uuid.Must(uuid.NewV7()))
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Errorf("a write with app.rls_bypass on and no tenant: err = %v, want an RLS violation (42501)", err)
		}
		if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT s`); err != nil {
			t.Fatal(err)
		}

		if _, err := tx.Exec(ctx, `SELECT set_config('app.rls_bypass', '', true)`); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `SELECT * FROM auth_lookup_user('nobody@example.com')`); err != nil {
			t.Fatal(err)
		}
		var after string
		if err := tx.QueryRow(ctx, `SELECT coalesce(current_setting('app.rls_bypass', true), '')`).Scan(&after); err != nil {
			t.Fatal(err)
		}
		if after == "on" {
			t.Error("app.rls_bypass is still on after a lookup returned")
		}
	})
}
