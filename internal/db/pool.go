// Package db owns every connection to PostgreSQL.
//
// It is the only package permitted to import pgx (see CLAUDE.md's import
// rules); go-arch-lint enforces that from P1-005 onward.
package db

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/miqbalhamdani/new-commerce-api/internal/platform/config"
)

// Store owns the connection pool and is the entry point for every query.
//
// tdd.md 3.2 and CLAUDE.md both call it Store: it is not merely a pool, it is
// the thing that hands out tenant-scoped transactions. See tx.go.
type Store struct {
	pool *pgxpool.Pool
}

// New opens a pool and verifies the server is reachable before returning it.
// A pool that cannot be reached is an error here rather than a surprise on the
// first query.
//
// Every connection runs in WIB (BR-007): the session TimeZone makes now(),
// date_trunc and date boundaries WIB in SQL, and the scan location makes every
// timestamptz arrive in Go -- and so leave as JSON -- with a +07:00 offset,
// whatever zone the process itself runs in.
func New(ctx context.Context, url string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse postgres url: %w", err)
	}
	cfg.ConnConfig.RuntimeParams["timezone"] = config.WIB.String()
	cfg.AfterConnect = func(_ context.Context, conn *pgx.Conn) error {
		conn.TypeMap().RegisterType(&pgtype.Type{Name: "timestamptz", OID: pgtype.TimestamptzOID,
			Codec: &pgtype.TimestamptzCodec{ScanLocation: config.WIB}})
		return nil
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open postgres pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return &Store{pool: pool}, nil
}

// Close releases every connection in the pool.
func (s *Store) Close() { s.pool.Close() }

// ServerVersion reports the version the server itself claims, e.g. "18.4" --
// which PostgreSQL was actually reached, not which one was configured.
//
// Homebrew and Debian builds append a packaging suffix ("18.4 (Homebrew)"), so
// only the leading version token is returned.
func (s *Store) ServerVersion(ctx context.Context) (string, error) {
	var v string
	if err := s.pool.QueryRow(ctx, "SHOW server_version").Scan(&v); err != nil {
		return "", fmt.Errorf("read server_version: %w", err)
	}
	if token, _, found := strings.Cut(v, " "); found {
		return token, nil
	}
	return v, nil
}
