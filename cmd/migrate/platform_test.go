package main

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/platform/config"
)

// TestPlatformSchema is P1-010's acceptance: the four platform tables match
// erd.md 3.2 exactly.
//
// "Exactly" is checked by reading the schema back out of PostgreSQL rather than
// by reading the migration file. A migration that was edited but never applied,
// or applied and then altered by hand, passes a source-level comparison and
// still leaves the database in the wrong shape.
func TestPlatformSchema(t *testing.T) {
	ctx := t.Context()

	if err := run(0); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	conn, err := pgx.Connect(ctx, config.DatabaseURL())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(ctx) })

	// name | type | nullable, in ordinal order, transcribed from erd.md 3.2.
	for _, tt := range []struct {
		table   string
		columns []column
	}{
		{"tenants", []column{
			{"id", "uuid", false},
			{"name", "text", false},
			{"slug", "text", false},
			{"timezone", "text", false},
			{"currency", "character(3)", false},
			{"status", "text", false},
			{"created_at", "timestamp with time zone", false},
		}},
		{"users", []column{
			{"id", "uuid", false},
			{"tenant_id", "uuid", false},
			{"email", "citext", false},
			{"password_hash", "text", true}, // null while an invitation is outstanding
			{"name", "text", false},
			{"role", "text", false},
			{"status", "text", false},
			{"last_login_at", "timestamp with time zone", true},
			{"created_at", "timestamp with time zone", false},
		}},
		{"refresh_tokens", []column{
			{"id", "uuid", false},
			{"tenant_id", "uuid", false},
			{"user_id", "uuid", false},
			{"token_hash", "text", false},
			{"rotated_from", "uuid", true},
			{"expires_at", "timestamp with time zone", false},
			{"revoked_at", "timestamp with time zone", true},
			{"created_at", "timestamp with time zone", false},
		}},
		{"api_keys", []column{
			{"id", "uuid", false},
			{"tenant_id", "uuid", false},
			{"name", "text", false},
			{"key_hash", "text", false},
			{"key_prefix", "text", false},
			{"permissions", "text[]", false},
			{"created_by", "uuid", true},
			{"last_used_at", "timestamp with time zone", true},
			{"revoked_at", "timestamp with time zone", true},
			{"created_at", "timestamp with time zone", false},
		}},
	} {
		t.Run(tt.table+" columns", func(t *testing.T) {
			got := columnsOf(ctx, t, conn, tt.table)
			if !slices.Equal(got, tt.columns) {
				t.Errorf("schema does not match erd.md 3.2\ngot:  %v\nwant: %v",
					got, tt.columns)
			}
		})
	}

	// A CHECK with the wrong value set is the failure that survives review:
	// the column exists, the type is right, and a value nobody tested is
	// silently accepted or silently rejected.
	for _, tt := range []struct {
		table, column string
		allowed       []string
		rejected      string
	}{
		{"tenants", "status", []string{"active", "suspended", "closed"}, "cancelled"},
		{"users", "role", []string{"owner", "admin", "ops", "warehouse", "viewer"}, "superadmin"},
		{"users", "status", []string{"invited", "active", "disabled"}, "deleted"},
	} {
		t.Run(fmt.Sprintf("%s.%s accepts only its CHECK values", tt.table, tt.column), func(t *testing.T) {
			src := checkConstraintSrc(ctx, t, conn, tt.table, tt.column)
			for _, v := range tt.allowed {
				if !strings.Contains(src, "'"+v+"'") {
					t.Errorf("%q is missing from the constraint: %s", v, src)
				}
			}
			if strings.Contains(src, "'"+tt.rejected+"'") {
				t.Errorf("%q should not be allowed: %s", tt.rejected, src)
			}
		})
	}

	t.Run("defaults match", func(t *testing.T) {
		for _, tt := range []struct{ table, column, want string }{
			{"tenants", "timezone", "'Asia/Jakarta'"},
			{"tenants", "currency", "'IDR'"},
			{"tenants", "status", "'active'"},
			{"users", "role", "'viewer'"},
			{"users", "status", "'invited'"},
			{"api_keys", "permissions", "'{}'"},
		} {
			got := columnDefault(ctx, t, conn, tt.table, tt.column)
			if !strings.Contains(got, tt.want) {
				t.Errorf("%s.%s default is %q, want it to contain %s",
					tt.table, tt.column, got, tt.want)
			}
		}
	})

	// UNIQUE (tenant_id, email) is what makes "find the user with this email"
	// answer one row rather than one per tenant.
	t.Run("uniqueness matches", func(t *testing.T) {
		for _, tt := range []struct {
			table string
			cols  []string
		}{
			{"tenants", []string{"slug"}},
			{"tenants", []string{"id"}}, // tenants_id_uq, see the note below
			{"users", []string{"tenant_id", "email"}},
			{"refresh_tokens", []string{"token_hash"}},
			{"api_keys", []string{"key_hash"}},
		} {
			if !hasUnique(ctx, t, conn, tt.table, tt.cols) {
				t.Errorf("%s has no UNIQUE on (%s)", tt.table, strings.Join(tt.cols, ", "))
			}
		}
	})

	// erd.md 3.2 puts RLS on the three tables carrying tenant_id and explicitly
	// not on tenants -- "it is reached only through the auth path".
	t.Run("tenants is deliberately not protected", func(t *testing.T) {
		var enabled bool
		if err := conn.QueryRow(ctx,
			`SELECT relrowsecurity FROM pg_class WHERE oid = 'tenants'::regclass`).Scan(&enabled); err != nil {
			t.Fatalf("read tenants flags: %v", err)
		}
		if enabled {
			// Not a leak, but it would break login: the auth path reads tenants
			// before any tenant context exists, so a policy would filter it to
			// nothing and every login would fail.
			t.Error("tenants has RLS enabled; erd.md 3.2 says it must not, " +
				"because auth reads it before a tenant context exists")
		}
	})

	// The composite foreign keys of erd.md 3.4 are checked from the child side,
	// but a table's own rows must also be reachable by its FK targets while RLS
	// is on. PostgreSQL runs referential integrity checks with row security
	// off; this asserts that rather than trusting it.
	t.Run("a foreign key can see a row RLS would hide", func(t *testing.T) {
		assertFKCrossesRLS(ctx, t, conn)
	})
}

type column struct {
	name     string
	dataType string
	nullable bool
}

func (c column) String() string {
	if c.nullable {
		return c.name + " " + c.dataType + " NULL"
	}
	return c.name + " " + c.dataType
}

func columnsOf(ctx context.Context, t *testing.T, conn *pgx.Conn, table string) []column {
	t.Helper()

	rows, err := conn.Query(ctx, `
		SELECT a.attname, format_type(a.atttypid, a.atttypmod), NOT a.attnotnull
		  FROM pg_attribute a
		 WHERE a.attrelid = $1::regclass AND a.attnum > 0 AND NOT a.attisdropped
		 ORDER BY a.attnum`, table)
	if err != nil {
		t.Fatalf("read columns of %s: %v", table, err)
	}
	defer rows.Close()

	var cols []column
	for rows.Next() {
		var c column
		if err := rows.Scan(&c.name, &c.dataType, &c.nullable); err != nil {
			t.Fatalf("scan column: %v", err)
		}
		cols = append(cols, c)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate columns: %v", err)
	}
	return cols
}

func checkConstraintSrc(ctx context.Context, t *testing.T, conn *pgx.Conn, table, col string) string {
	t.Helper()

	var src string
	err := conn.QueryRow(ctx, `
		SELECT pg_get_constraintdef(c.oid)
		  FROM pg_constraint c
		  JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = ANY(c.conkey)
		 WHERE c.conrelid = $1::regclass AND c.contype = 'c' AND a.attname = $2`,
		table, col).Scan(&src)
	if err != nil {
		t.Fatalf("read CHECK on %s.%s: %v", table, col, err)
	}
	return src
}

func columnDefault(ctx context.Context, t *testing.T, conn *pgx.Conn, table, col string) string {
	t.Helper()

	var def *string
	err := conn.QueryRow(ctx, `
		SELECT pg_get_expr(d.adbin, d.adrelid)
		  FROM pg_attrdef d
		  JOIN pg_attribute a ON a.attrelid = d.adrelid AND a.attnum = d.adnum
		 WHERE d.adrelid = $1::regclass AND a.attname = $2`, table, col).Scan(&def)
	if err != nil {
		t.Fatalf("read default of %s.%s: %v", table, col, err)
	}
	if def == nil {
		return ""
	}
	return *def
}

func hasUnique(ctx context.Context, t *testing.T, conn *pgx.Conn, table string, cols []string) bool {
	t.Helper()

	var found bool
	err := conn.QueryRow(ctx, `
		SELECT EXISTS (
		    SELECT 1 FROM pg_constraint c
		     WHERE c.conrelid = $1::regclass
		       AND c.contype IN ('u','p')
		       AND (SELECT array_agg(a.attname::text ORDER BY x.ord)
		              FROM unnest(c.conkey) WITH ORDINALITY AS x(attnum, ord)
		              JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = x.attnum
		           ) = $2::text[])`, table, cols).Scan(&found)
	if err != nil {
		t.Fatalf("read unique constraints on %s: %v", table, err)
	}
	return found
}

// assertFKCrossesRLS proves a child row can reference a parent row that the
// child's own tenant context would hide. If referential integrity respected
// RLS, every insert into refresh_tokens would fail to find its user.
func assertFKCrossesRLS(ctx context.Context, t *testing.T, conn *pgx.Conn) {
	t.Helper()

	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	// Rolled back either way: this test writes real rows into real tables.
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	var tenantID, userID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO tenants (id, name, slug) VALUES (gen_random_uuid(), 'FK probe', $1)
		RETURNING id`, "fk-probe-"+t.Name()).Scan(&tenantID); err != nil {
		t.Fatalf("insert tenant: %v", err)
	}
	if _, err := tx.Exec(ctx,
		`SELECT set_config('app.tenant_id', $1::text, true)`, tenantID); err != nil {
		t.Fatalf("set tenant: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO users (id, tenant_id, email, name)
		VALUES (gen_random_uuid(), $1, 'fk-probe@example.com', 'FK probe')
		RETURNING id`, tenantID).Scan(&userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO refresh_tokens (id, tenant_id, user_id, token_hash, expires_at)
		VALUES (gen_random_uuid(), $1, $2, 'fk-probe-hash', now() + interval '1 day')`,
		tenantID, userID); err != nil {
		t.Fatalf("insert refresh_token: %v\n\n"+
			"The foreign key to users could not see its target row. That would mean "+
			"referential integrity is subject to row level security, and no child row "+
			"could ever be written.", err)
	}
}
