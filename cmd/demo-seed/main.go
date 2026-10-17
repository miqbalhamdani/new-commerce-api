// Command demo-seed opens a demo shop on a live stack and fills every admin
// screen: a team in each role and status, settings and API keys, brands, a
// category tree, products in each status with placeholder photos, customers,
// and orders in every state with their payments and history
// (deploy/README.md, "A demo shop").
//
//	demo-seed -owner-email demo@belli.id
//
// Like create-tenant it connects as the app (APP_DATABASE_URL) and prints one
// password on stdout, which every active demo login shares. It also needs the
// bucket (S3_*) and Redis: photos are uploaded and confirmed as the admin's
// uploads are, and the worker makes their derivatives.
//
// It sends no email. Staff are inserted, never invited; orders go through
// the order service with no mailer, and their customer snapshots carry no
// address, so a demo click on Mark paid or Ship has nobody to write to.
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
	"time"

	"github.com/google/uuid"

	"github.com/miqbalhamdani/new-commerce-api/internal/catalog"
	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/jobs"
	"github.com/miqbalhamdani/new-commerce-api/internal/orders"
	"github.com/miqbalhamdani/new-commerce-api/internal/platform/config"
	"github.com/miqbalhamdani/new-commerce-api/internal/platform/logging"
	"github.com/miqbalhamdani/new-commerce-api/internal/queue"
	"github.com/miqbalhamdani/new-commerce-api/internal/storage"
	"github.com/miqbalhamdani/new-commerce-api/internal/team"
)

type shop struct {
	Name, Slug, OrderPrefix, OwnerEmail string
}

var (
	orderPrefixRE = regexp.MustCompile(`^[A-Z0-9]{2,6}$`)
	slugRE        = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
)

func main() {
	slog.SetDefault(logging.New(os.Stderr))
	var s shop
	flag.StringVar(&s.Name, "name", "Toko Demo Belli", "shop name")
	flag.StringVar(&s.Slug, "slug", "demo", "shop slug; staff sign in at <name>@<slug>.example.com")
	flag.StringVar(&s.OrderPrefix, "order-prefix", "DEMO", "order number prefix: 2-6 of A-Z and 0-9")
	flag.StringVar(&s.OwnerEmail, "owner-email", "", "the demo owner's sign-in email")
	flag.Parse()

	if err := run(s); err != nil {
		slog.Error("demo-seed failed", "error", err)
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
	files, err := storage.FromEnv()
	if err != nil {
		return err
	}
	q, err := queue.New(ctx, config.RedisURL())
	if err != nil {
		return err
	}
	defer func() { _ = q.Close() }()

	password := rand.Text()
	out, err := newSeeder(store, files, q).seed(ctx, s, password, time.Now())
	if err != nil {
		return err
	}
	// stdout, not the log: the password must not land in a log pipeline.
	fmt.Printf("created demo shop %q (%s)\n", s.Name, out.tenantID)
	fmt.Printf("password for every login below: %s\n", password)
	for _, l := range out.logins {
		fmt.Println("  " + l)
	}
	fmt.Printf("%d products, %d photos, %d customers, %d orders\n", out.products, out.photos, out.customers, out.orders)
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
	return errors.Join(errs...)
}

type seeder struct {
	store   *db.Store
	files   *storage.Store
	catalog *catalog.Service
	team    *team.Service
	orders  *orders.Service
}

func newSeeder(store *db.Store, files *storage.Store, q *queue.Client) *seeder {
	return &seeder{
		store:   store,
		files:   files,
		catalog: catalog.NewService(store, files, jobs.NewService(store, q)),
		// No queue: nothing here invites anyone, and Invite is what would send.
		team: team.NewService(store, nil, nil),
		// No mailer (Mail stays nil): no order email is queued.
		orders: orders.NewService(store, nil, nil),
	}
}

// summary is what a run made, for stdout and the test.
type summary struct {
	tenantID                            uuid.UUID
	logins                              []string
	products, photos, customers, orders int
}

// seed builds the shop in the order a real one fills up: team and settings,
// the catalog, then customers and three months of orders. The catalog is
// moved back to before the first order once it is in; each order is moved
// back as it is placed. The last steps archive what a shop retires.
func (sd *seeder) seed(ctx context.Context, s shop, password string, now time.Time) (summary, error) {
	t, err := sd.shopAndTeam(ctx, s, password, now)
	if err != nil {
		return summary{}, fmt.Errorf("shop and team: %w", err)
	}
	out := summary{tenantID: t.tenantID, logins: t.logins}
	if err := sd.settings(t.as(t.owner), t); err != nil {
		return out, fmt.Errorf("settings: %w", err)
	}
	cat, err := sd.seedCatalog(t.as(t.admin), now)
	if err != nil {
		return out, fmt.Errorf("catalog: %w", err)
	}
	out.products, out.photos = len(cat.products), cat.photos
	if err := sd.shiftBack(t.ctx, 98*day); err != nil {
		return out, fmt.Errorf("backdate catalog: %w", err)
	}
	ids, err := sd.insertCustomers(t.ctx, now)
	if err != nil {
		return out, fmt.Errorf("customers: %w", err)
	}
	out.customers = len(ids)
	if out.orders, err = sd.seedOrders(t, cat, ids, now); err != nil {
		return out, fmt.Errorf("orders: %w", err)
	}
	if err := sd.retire(t, cat); err != nil {
		return out, fmt.Errorf("archive: %w", err)
	}
	slog.Info(fmt.Sprintf("seeded demo shop: %d products, %d photos, %d customers, %d orders",
		out.products, out.photos, out.customers, out.orders))
	return out, nil
}
