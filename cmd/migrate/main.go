// Command migrate applies the database migrations and exits.
//
// It is a separate binary from cmd/api on purpose: schema changes run once, as
// the schema owner, before the API rolls. The API connects as app_user, which
// owns nothing and could not apply a migration if it tried.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/db/migrations"
	"github.com/miqbalhamdani/new-commerce-api/internal/platform/config"
	"github.com/miqbalhamdani/new-commerce-api/internal/platform/logging"
)

func main() {
	slog.SetDefault(logging.New(os.Stderr))
	down := flag.Int("down", 0, "roll back this many migrations instead of applying. Local use only -- production is forward-only.")
	flag.Parse()

	if err := run(*down); err != nil {
		slog.Error("migrate failed", "error", err)
		os.Exit(1)
	}
}

func run(down int) error {
	mode := config.DBRoleMode()
	if mode != "strong" && mode != "limited" {
		return fmt.Errorf("DB_ROLE_MODE is %q; want strong or limited", mode)
	}
	if mode == "limited" {
		if err := refuseNeedlessLimited(config.DatabaseURL()); err != nil {
			return err
		}
	}
	m, err := open(config.DatabaseURL(), mode)
	if err != nil {
		return err
	}
	// Close reports the source and database errors separately; neither is
	// actionable here beyond logging, and the migration itself has already
	// committed or rolled back.
	defer func() {
		if srcErr, dbErr := m.Close(); srcErr != nil || dbErr != nil {
			slog.Warn("closing migrator", "source", srcErr, "database", dbErr)
		}
	}()

	if down > 0 {
		err = m.Steps(-down)
	} else {
		err = m.Up()
	}
	// Already at the target version. Not a failure -- migrate runs on every
	// deploy and most deploys change no schema.
	if err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}

	version, dirty, err := m.Version()
	if errors.Is(err, migrate.ErrNilVersion) {
		slog.Info("no migrations applied")
		return nil
	}
	if err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if dirty {
		// A migration failed part-way and the version was not committed. Nothing
		// automatic can fix this: someone has to look at what half-applied.
		return fmt.Errorf("schema is dirty at version %d -- inspect and force a version before retrying", version)
	}
	if err := checkRoleMode(config.DatabaseURL(), mode); err != nil {
		return err
	}
	slog.Info("migrations applied in "+mode+" role mode", "version", version)
	return nil
}

// refuseNeedlessLimited is the first migration's own refusal, made before
// anything runs: where the migrating role could create app_user and
// auth_lookup, limited mode is a choice of convenience, never a fallback
// (BR-001). Failing inside the migration would also leave it dirty.
func refuseNeedlessLimited(dsn string) error {
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connect to check the role mode: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	var capable bool
	if err := conn.QueryRow(ctx, `SELECT rolsuper OR (rolcreaterole AND rolbypassrls)
		FROM pg_roles WHERE rolname = current_user`).Scan(&capable); err != nil {
		return fmt.Errorf("read the migrating role: %w", err)
	}
	if capable {
		return errors.New("DB_ROLE_MODE is limited, but this role can create app_user and auth_lookup -- " +
			"use the default mode (BR-001)")
	}
	return nil
}

// checkRoleMode fails when the database was created in a different mode than
// DB_ROLE_MODE asks for. The first migration freezes the mode into
// db_role_mode() (BR-001), so a later DB_ROLE_MODE changes nothing -- and an
// API configured for one mode against a schema built for the other would
// either fail every sign-in or run as a role it was never meant to.
func checkRoleMode(dsn, want string) error {
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connect to check the role mode: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	var got string
	if err := conn.QueryRow(ctx, `SELECT db_role_mode()`).Scan(&got); err != nil {
		return fmt.Errorf("read db_role_mode(): %w", err)
	}
	if got != want {
		return fmt.Errorf("the schema was created in %s mode but DB_ROLE_MODE is %s -- "+
			"the mode is fixed when the schema is created (BR-001)", got, want)
	}
	return nil
}

// open builds a migrator over the embedded SQL files. The role mode travels as
// the session setting app.role_mode, which only the first migration reads.
func open(dsn, mode string) (*migrate.Migrate, error) {
	src, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return nil, fmt.Errorf("read embedded migrations: %w", err)
	}

	// golang-migrate picks its database driver from the URL scheme, and the
	// pgx/v5 driver registers itself as "pgx5". Everything else in this repo
	// speaks postgres://, so rewrite the scheme rather than keeping a second
	// DSN in the environment purely for this binary.
	u, err := url.Parse(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	u.Scheme = "pgx5"
	q := u.Query()
	q.Set("options", strings.TrimSpace(q.Get("options")+" -c app.role_mode="+mode))
	u.RawQuery = q.Encode()

	m, err := migrate.NewWithSourceInstance("iofs", src, u.String())
	if err != nil {
		return nil, fmt.Errorf("open migrator: %w", err)
	}
	return m, nil
}
