package main

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/db/migrations"
	"github.com/miqbalhamdani/new-commerce-api/internal/platform/config"
)

// TestFourRolesMigrationRefusesWarehouse is P1-017's acceptance: the migration
// fails loudly while any user still holds warehouse, instead of remapping them.
//
// It replays 000004's SQL inside a transaction that first restores the v1
// constraint and seeds a warehouse user, then rolls everything back -- the
// shared database never sees the user.
func TestFourRolesMigrationRefusesWarehouse(t *testing.T) {
	ctx := t.Context()
	if err := run(0); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	conn, err := pgx.Connect(ctx, config.DatabaseURL())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(ctx) })

	up, err := migrations.FS.ReadFile("000004_four_roles.up.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	down, err := migrations.FS.ReadFile("000004_four_roles.down.sql")
	if err != nil {
		t.Fatalf("read down migration: %v", err)
	}

	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tenantID, userID := uuid.New(), uuid.New()
	for _, stmt := range []string{
		string(down),
		`INSERT INTO tenants (id, name, slug, order_prefix) VALUES ('` + tenantID.String() + `', 'Warehouse Co', 'wh-` + tenantID.String() + `', 'WHC')`,
		`INSERT INTO users (id, tenant_id, email, name, role) VALUES ('` + userID.String() + `', '` +
			tenantID.String() + `', 'wh-` + userID.String() + `@example.com', 'Wawan', 'warehouse')`,
	} {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			t.Fatalf("set up v1 state: %v", err)
		}
	}

	_, err = tx.Exec(ctx, string(up))
	if err == nil {
		t.Fatal("migration succeeded with a warehouse user present; it must refuse")
	}
	if !strings.Contains(err.Error(), "warehouse") {
		t.Errorf("error does not say why: %v", err)
	}
}
