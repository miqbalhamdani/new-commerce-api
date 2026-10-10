// Command dev-seed creates a shop and its owner on a developer's machine, so
// the admin has someone to sign in as. There is no signup API -- tenants are
// created by the platform team (04-api-spec.md §2) -- and this is the
// development stand-in for that. It refuses to run anywhere else.
//
//	make dev-seed   # owner@example.com / development-password
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/platform/config"
	"github.com/miqbalhamdani/new-commerce-api/internal/platform/logging"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

const (
	email    = "owner@example.com"
	password = "development-password"
)

func main() {
	slog.SetDefault(logging.New(os.Stderr))
	if err := run(); err != nil {
		slog.Error("dev-seed failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	if !config.IsDevelopment() {
		return errors.New("dev-seed runs only when ENVIRONMENT is development")
	}
	ctx := context.Background()
	store, err := db.New(ctx, config.AppDatabaseURL())
	if err != nil {
		return err
	}
	defer store.Close()

	owner, err := store.LookupUserForAuth(ctx, email)
	if err == nil {
		slog.Info("shop already seeded; sign in with owner@example.com / development-password")
	} else {
		if owner, err = createShop(ctx, store); err != nil {
			return err
		}
		slog.Info("seeded Demo Shop; sign in with owner@example.com / development-password")
	}
	return seedSamples(ctx, store, owner)
}

// createShop makes the Demo Shop and its owner, then reads the owner back the
// way sign-in does.
func createShop(ctx context.Context, store *db.Store) (db.AuthUser, error) {
	hash, err := auth.HashPassword(password)
	if err != nil {
		return db.AuthUser{}, err
	}
	tenantID, userID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	err = store.InTenantTx(tenant.NewContext(ctx, tenantID), func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO tenants (id, name, slug, order_prefix) VALUES ($1, 'Demo Shop', $2, 'DMO')`,
			tenantID, "demo-"+tenantID.String()[:8]); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO users (id, tenant_id, email, password_hash, name, role, status)
			VALUES ($1, $2, $3, $4, 'Demo Owner', 'owner', 'active')`, userID, tenantID, email, hash)
		return err
	})
	if err != nil {
		return db.AuthUser{}, err
	}
	return store.LookupUserForAuth(ctx, email)
}
