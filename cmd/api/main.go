// Command api serves the Phase 1 HTTP API.
//
// At P1-000 it exposes only /healthz -- enough to prove the process reaches
// PostgreSQL and Redis. Documented endpoints arrive with the generated server
// interface in P1-005.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/queue"
)

// Defaults target a host install -- Homebrew's postgresql@18 and redis on their
// standard ports (contracts/tdd.md 2.2). They are compiled in so that a clean
// checkout runs without a .env; anything else overrides via the environment.
const (
	defaultDatabaseURL = "postgres://localhost:5432/new_commerce_dev?sslmode=disable"
	defaultRedisURL    = "redis://localhost:6379/0"
	defaultPort        = "8080"
)

func main() {
	if err := run(); err != nil {
		slog.Error("api exited", "error", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := db.New(ctx, getenv("DATABASE_URL", defaultDatabaseURL))
	if err != nil {
		return err
	}
	defer pool.Close()

	redis, err := queue.New(ctx, getenv("REDIS_URL", defaultRedisURL))
	if err != nil {
		return err
	}
	defer func() { _ = redis.Close() }()

	mux := http.NewServeMux()
	mux.Handle("GET /healthz", newHealthHandler(
		checker{name: "postgres", version: pool.ServerVersion},
		checker{name: "redis", version: redis.ServerVersion},
	))

	addr := ":" + getenv("PORT", defaultPort)
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	serveErr := make(chan error, 1)
	go func() {
		slog.Info("api listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
	}()

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
	}

	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
