package main

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/platform/config"
)

// TestCatalogSchema is the schema half of the Phase 1 catalog items: each
// table matches 03-erd.md §3.3 column for column, read back from PostgreSQL.
func TestCatalogSchema(t *testing.T) {
	ctx := t.Context()
	conn := migratedOwner(ctx, t)

	for _, tt := range []struct {
		table   string
		columns []column
	}{
		{"brands", []column{
			{"id", "uuid", false},
			{"tenant_id", "uuid", false},
			{"name", "text", false},
			{"slug", "text", false},
			{"archived_at", "timestamp with time zone", true},
			{"created_at", "timestamp with time zone", false},
			{"updated_at", "timestamp with time zone", false},
		}},
		{"categories", []column{
			{"id", "uuid", false},
			{"tenant_id", "uuid", false},
			{"parent_id", "uuid", true},
			{"kind", "text", false},
			{"name", "text", false},
			{"path", "ltree", false},
			{"archived_at", "timestamp with time zone", true},
			{"created_at", "timestamp with time zone", false},
			{"updated_at", "timestamp with time zone", false},
		}},
		{"products", []column{
			{"id", "uuid", false},
			{"tenant_id", "uuid", false},
			{"title", "text", false},
			{"slug", "text", false},
			{"description", "text", true},
			{"brand_id", "uuid", true},
			{"status", "text", false},
			{"attributes", "jsonb", false},
			{"option_names", "text[]", false},
			{"version", "integer", false},
			{"archived_at", "timestamp with time zone", true},
			{"created_at", "timestamp with time zone", false},
			{"updated_at", "timestamp with time zone", false},
		}},
	} {
		t.Run(tt.table+" columns", func(t *testing.T) {
			if got := columnsOf(ctx, t, conn, tt.table); !slices.Equal(got, tt.columns) {
				t.Errorf("schema does not match 03-erd.md §3.3\ngot:  %v\nwant: %v", got, tt.columns)
			}
		})
	}

	// P1-020 / BR-030: the slug is unique per tenant, archived brands
	// included, and free to repeat across tenants.
	t.Run("brand slug unique per tenant including archived", func(t *testing.T) {
		tx := beginRolledBack(ctx, t, conn)
		a, b := seedTenant(ctx, t, tx), seedTenant(ctx, t, tx)
		insert := func(tenantID uuid.UUID, archived bool) error {
			_, err := tx.Exec(ctx, `SAVEPOINT s`)
			if err != nil {
				t.Fatalf("savepoint: %v", err)
			}
			_, err = tx.Exec(ctx, `INSERT INTO brands (id, tenant_id, name, slug, archived_at)
				VALUES ($1, $2, 'Erigo', slugify('Erigo'), CASE WHEN $3 THEN now() END)`,
				uuid.Must(uuid.NewV7()), tenantID, archived)
			if err != nil {
				_, _ = tx.Exec(ctx, `ROLLBACK TO SAVEPOINT s`)
			}
			return err
		}
		if err := insert(a, true); err != nil {
			t.Fatalf("first brand: %v", err)
		}
		if err := insert(a, false); err == nil {
			t.Error("a second 'erigo' slug in one tenant was accepted while the first is archived")
		}
		if err := insert(b, false); err != nil {
			t.Errorf("the same slug at another tenant was refused: %v", err)
		}
	})

	// BR-017: stock is not tracked in any form, so no catalog table carries a
	// quantity, stock or inventory column -- now or in any later migration.
	t.Run("no quantity column anywhere", func(t *testing.T) {
		rows, err := conn.Query(ctx, `SELECT table_name || '.' || column_name FROM information_schema.columns
			WHERE table_schema = 'public' AND column_name ~ '(qty|quantity|stock|inventory)'
			  AND table_name IN ('brands','categories','products','variants','product_categories','product_media')`)
		if err != nil {
			t.Fatalf("query columns: %v", err)
		}
		defer rows.Close()
		for rows.Next() {
			var c string
			_ = rows.Scan(&c)
			t.Errorf("%s exists; BR-017 forbids stock in any form", c)
		}
	})

	// BR-042: a product slug is unique per tenant, archived products included.
	// BR-004: a product's brand belongs to the same tenant.
	t.Run("product slug and brand", func(t *testing.T) {
		tx := beginRolledBack(ctx, t, conn)
		a, b := seedTenant(ctx, t, tx), seedTenant(ctx, t, tx)
		brandB := uuid.Must(uuid.NewV7())
		if _, err := tx.Exec(ctx, `INSERT INTO brands (id, tenant_id, name, slug) VALUES ($1, $2, 'B', 'b')`, brandB, b); err != nil {
			t.Fatalf("brand: %v", err)
		}
		insert := func(tenantID uuid.UUID, slug string, brand *uuid.UUID, archived bool) error {
			if _, err := tx.Exec(ctx, `SAVEPOINT s`); err != nil {
				t.Fatalf("savepoint: %v", err)
			}
			_, err := tx.Exec(ctx, `INSERT INTO products (id, tenant_id, title, slug, brand_id, archived_at)
				VALUES ($1, $2, 'Tee', $3, $4, CASE WHEN $5 THEN now() END)`,
				uuid.Must(uuid.NewV7()), tenantID, slug, brand, archived)
			if err != nil {
				_, _ = tx.Exec(ctx, `ROLLBACK TO SAVEPOINT s`)
			}
			return err
		}
		if err := insert(a, "tee", nil, true); err != nil {
			t.Fatalf("first product: %v", err)
		}
		if insert(a, "tee", nil, false) == nil {
			t.Error("a second 'tee' slug in one tenant was accepted while the first is archived")
		}
		if err := insert(b, "tee", &brandB, false); err != nil {
			t.Errorf("same slug at another tenant, own brand: %v", err)
		}
		if insert(a, "other-tee", &brandB, false) == nil {
			t.Error("a product took another tenant's brand")
		}
	})

	t.Run("slugify folds accents and punctuation", func(t *testing.T) {
		for in, want := range map[string]string{
			"Erigo":              "erigo",
			"  Café Ñoño & Co. ": "cafe-nono-co",
			"Basic Tee 30s":      "basic-tee-30s",
			"---":                "",
		} {
			var got string
			if err := conn.QueryRow(ctx, `SELECT slugify($1)`, in).Scan(&got); err != nil {
				t.Fatalf("slugify(%q): %v", in, err)
			}
			if got != want {
				t.Errorf("slugify(%q) = %q, want %q", in, got, want)
			}
		}
	})
}

func migratedOwner(ctx context.Context, t *testing.T) *pgx.Conn {
	t.Helper()
	if err := run(0); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	conn, err := pgx.Connect(ctx, config.DatabaseURL())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(context.WithoutCancel(ctx)) })
	return conn
}

// beginRolledBack opens a transaction on the owner connection that is always
// rolled back, so schema probes never leave rows in the shared database.
func beginRolledBack(ctx context.Context, t *testing.T, conn *pgx.Conn) pgx.Tx {
	t.Helper()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.WithoutCancel(ctx)) })
	return tx
}

func seedTenant(ctx context.Context, t *testing.T, tx pgx.Tx) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	if _, err := tx.Exec(ctx, `INSERT INTO tenants (id, name, slug, order_prefix)
		VALUES ($1, 'Schema probe', $2, 'SCH')`, id, "probe-"+id.String()); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	return id
}

// TestCategoryPaths is P1-022's acceptance: the trigger derives path from name
// and parent, and one UPDATE of a node rewrites every descendant (BR-032).
func TestCategoryPaths(t *testing.T) {
	ctx := t.Context()
	tx := beginRolledBack(ctx, t, migratedOwner(ctx, t))
	tenantID := seedTenant(ctx, t, tx)

	add := func(name, kind string, parent *uuid.UUID) uuid.UUID {
		t.Helper()
		id := uuid.Must(uuid.NewV7())
		if _, err := tx.Exec(ctx, `INSERT INTO categories (id, tenant_id, parent_id, kind, name)
			VALUES ($1, $2, $3, $4, $5)`, id, tenantID, parent, kind, name); err != nil {
			t.Fatalf("insert %s: %v", name, err)
		}
		return id
	}
	pathOf := func(id uuid.UUID) string {
		t.Helper()
		var p string
		if err := tx.QueryRow(ctx, `SELECT path::text FROM categories WHERE id = $1`, id).Scan(&p); err != nil {
			t.Fatalf("read path: %v", err)
		}
		return p
	}

	apparel := add("Apparel", "category", nil)
	outer := add("Outerwear", "category", &apparel)
	jackets := add("Jackets", "category", &outer)
	rain := add("Rain Jackets", "category", &jackets)
	// A second tree of another kind with the same label path must not move.
	seriesApparel := add("Apparel", "series", nil)
	seriesOuter := add("Outerwear", "series", &seriesApparel)

	if got := pathOf(rain); got != "apparel.outerwear.jackets.rain_jackets" {
		t.Fatalf("derived path %q", got)
	}

	technical := add("Technical", "category", &apparel)
	if _, err := tx.Exec(ctx, `UPDATE categories SET parent_id = $1 WHERE id = $2`, technical, outer); err != nil {
		t.Fatalf("move: %v", err)
	}
	if got := pathOf(rain); got != "apparel.technical.outerwear.jackets.rain_jackets" {
		t.Errorf("after one move, descendant path %q", got)
	}
	if _, err := tx.Exec(ctx, `UPDATE categories SET name = 'Coats & Jackets' WHERE id = $1`, jackets); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if got := pathOf(rain); got != "apparel.technical.outerwear.coats_jackets.rain_jackets" {
		t.Errorf("after rename, descendant path %q", got)
	}
	if got := pathOf(seriesOuter); got != "apparel.outerwear" {
		t.Errorf("the series tree moved with the category tree: %q", got)
	}

	refused := func(what, sql string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(ctx, `SAVEPOINT s`); err != nil {
			t.Fatalf("savepoint: %v", err)
		}
		if _, err := tx.Exec(ctx, sql, args...); err == nil {
			t.Errorf("%s was accepted", what)
		}
		if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT s`); err != nil {
			t.Fatalf("rollback to savepoint: %v", err)
		}
	}
	refused("a parent of another kind", `INSERT INTO categories (id, tenant_id, parent_id, kind, name)
		VALUES ($1, $2, $3, 'category', 'Mixed')`, uuid.Must(uuid.NewV7()), tenantID, seriesApparel)
	other := seedTenant(ctx, t, tx)
	refused("a parent at another tenant (BR-004)", `INSERT INTO categories (id, tenant_id, parent_id, kind, name)
		VALUES ($1, $2, $3, 'category', 'Foreign')`, uuid.Must(uuid.NewV7()), other, apparel)
}

// TestCategoryGuards is P1-023's acceptance: moving a node beneath its own
// descendant errors (BR-034), and siblings named alike get _1 labels (BR-035).
func TestCategoryGuards(t *testing.T) {
	ctx := t.Context()
	tx := beginRolledBack(ctx, t, migratedOwner(ctx, t))
	tenantID := seedTenant(ctx, t, tx)

	add := func(name string, parent *uuid.UUID) (uuid.UUID, string) {
		t.Helper()
		id := uuid.Must(uuid.NewV7())
		var path string
		if err := tx.QueryRow(ctx, `INSERT INTO categories (id, tenant_id, parent_id, name)
			VALUES ($1, $2, $3, $4) RETURNING path::text`, id, tenantID, parent, name).Scan(&path); err != nil {
			t.Fatalf("insert %s: %v", name, err)
		}
		return id, path
	}

	root, _ := add("Apparel", nil)
	_, first := add("Jackets", &root)
	_, second := add("jackets!", &root)
	_, third := add("Jackets", &root)
	if first != "apparel.jackets" || second != "apparel.jackets_1" || third != "apparel.jackets_2" {
		t.Errorf("sibling paths %s, %s, %s", first, second, third)
	}

	child, _ := add("Kids", &root)
	grandchild, _ := add("Tees", &child)
	for _, target := range []uuid.UUID{grandchild, root} {
		if _, err := tx.Exec(ctx, `SAVEPOINT s`); err != nil {
			t.Fatalf("savepoint: %v", err)
		}
		_, err := tx.Exec(ctx, `UPDATE categories SET parent_id = $1 WHERE id = $2`, target, root)
		if err == nil || !strings.Contains(err.Error(), "beneath its own descendant") {
			t.Errorf("moving the root beneath %s: %v", target, err)
		}
		if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT s`); err != nil {
			t.Fatalf("rollback to savepoint: %v", err)
		}
	}
}
