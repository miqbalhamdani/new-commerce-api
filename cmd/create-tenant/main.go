// Command create-tenant creates a shop and its owner in production. There is no
// signup API -- tenants are created by the platform team (04-api-spec.md §2) --
// and dev-seed refuses to run outside development, so this is the one way a
// shop comes into being on a live stack.
//
//	create-tenant -name "Belli" -slug belli -order-prefix BLI \
//	    -owner-email owner@belli.id -owner-name "Belli Owner"
//
// It connects as app_user (APP_DATABASE_URL), like the API, and writes inside
// the new shop's own InTenantTx. The owner gets a random password, printed once
// on stdout; staff invitations take over from there.
package main

import (
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"regexp"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/platform/config"
	"github.com/miqbalhamdani/new-commerce-api/internal/platform/logging"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

// shop is what the platform team supplies for a new tenant.
type shop struct {
	Name, Slug, OrderPrefix, OwnerEmail, OwnerName string
}

var (
	// The tenants_order_prefix_check constraint (BR-077), checked here first so
	// a typo is a readable error rather than a constraint name.
	orderPrefixRE = regexp.MustCompile(`^[A-Z0-9]{2,6}$`)
	slugRE        = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
)

func main() {
	slog.SetDefault(logging.New(os.Stderr))
	var s shop
	flag.StringVar(&s.Name, "name", "", "shop name")
	flag.StringVar(&s.Slug, "slug", "", "shop slug: lowercase letters, digits and hyphens")
	flag.StringVar(&s.OrderPrefix, "order-prefix", "", "order number prefix: 2-6 of A-Z and 0-9")
	flag.StringVar(&s.OwnerEmail, "owner-email", "", "the owner's sign-in email")
	flag.StringVar(&s.OwnerName, "owner-name", "", "the owner's name")
	flag.Parse()

	if err := run(s); err != nil {
		slog.Error("create-tenant failed", "error", err)
		os.Exit(1)
	}
}

func run(s shop) error {
	if err := s.validate(); err != nil {
		return err
	}
	ctx := context.Background()
	store, err := db.New(ctx, config.AppDatabaseURL())
	if err != nil {
		return err
	}
	defer store.Close()

	password := rand.Text()
	tenantID, err := create(ctx, store, s, password)
	if err != nil {
		return err
	}
	// stdout, not the log: the password must not land in a log pipeline.
	fmt.Printf("created shop %q (%s)\nsign in as %s with password: %s\n", s.Name, tenantID, s.OwnerEmail, password)
	return nil
}

func (s shop) validate() error {
	var errs []error
	if s.Name == "" {
		errs = append(errs, errors.New("-name is required"))
	}
	if !slugRE.MatchString(s.Slug) {
		errs = append(errs, fmt.Errorf("-slug %q must be lowercase letters, digits and hyphens", s.Slug))
	}
	if !orderPrefixRE.MatchString(s.OrderPrefix) {
		errs = append(errs, fmt.Errorf("-order-prefix %q must be 2-6 of A-Z and 0-9", s.OrderPrefix))
	}
	if s.OwnerEmail == "" {
		errs = append(errs, errors.New("-owner-email is required"))
	}
	if s.OwnerName == "" {
		errs = append(errs, errors.New("-owner-name is required"))
	}
	return errors.Join(errs...)
}

// create inserts the shop and its active owner in one transaction, so a taken
// slug or email leaves nothing behind.
func create(ctx context.Context, store *db.Store, s shop, password string) (uuid.UUID, error) {
	hash, err := auth.HashPassword(password)
	if err != nil {
		return uuid.Nil, err
	}
	tenantID, userID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	err = store.InTenantTx(tenant.NewContext(ctx, tenantID), func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO tenants (id, name, slug, order_prefix) VALUES ($1, $2, $3, $4)`,
			tenantID, s.Name, s.Slug, s.OrderPrefix); err != nil {
			return fmt.Errorf("insert shop: %w", err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO users (id, tenant_id, email, password_hash, name, role, status)
			VALUES ($1, $2, $3, $4, $5, 'owner', 'active')`, userID, tenantID, s.OwnerEmail, hash, s.OwnerName); err != nil {
			return fmt.Errorf("insert owner: %w", err)
		}
		return nil
	})
	if err != nil {
		return uuid.Nil, err
	}
	return tenantID, nil
}
