// Command api serves the HTTP API: /healthz, and the /v1 routes generated
// from contracts/openapi.yaml.
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

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/catalog"
	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	httpapi "github.com/miqbalhamdani/new-commerce-api/internal/http"
	"github.com/miqbalhamdani/new-commerce-api/internal/jobs"
	"github.com/miqbalhamdani/new-commerce-api/internal/orders"
	"github.com/miqbalhamdani/new-commerce-api/internal/platform/config"
	"github.com/miqbalhamdani/new-commerce-api/internal/platform/logging"
	"github.com/miqbalhamdani/new-commerce-api/internal/platform/telemetry"
	"github.com/miqbalhamdani/new-commerce-api/internal/queue"
	"github.com/miqbalhamdani/new-commerce-api/internal/storage"
	"github.com/miqbalhamdani/new-commerce-api/internal/team"
)

func main() {
	slog.SetDefault(logging.New(os.Stderr))
	// time.Now() and anything built from it serialises as +07:00, like the
	// timestamps the pool scans (BR-007).
	time.Local = config.WIB
	if err := run(); err != nil {
		slog.Error("api exited", "error", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Before anything that can fail with a trace id in it.
	flushTraces, err := telemetry.Setup(ctx,
		config.ServiceName(), config.ServiceVersion(), config.Environment(), config.OTLPEndpoint())
	if err != nil {
		return err
	}
	defer func() {
		if err := flushTraces(context.WithoutCancel(ctx)); err != nil {
			slog.Warn("flushing traces", "error", err)
		}
	}()

	pool, err := db.New(ctx, config.AppDatabaseURL())
	if err != nil {
		return err
	}
	defer pool.Close()

	redis, err := queue.New(ctx, config.RedisURL())
	if err != nil {
		return err
	}
	defer func() { _ = redis.Close() }()

	files, err := storage.FromEnv()
	if err != nil {
		return err
	}
	jobsSvc := jobs.NewService(pool, redis)

	secret, err := config.JWTSecret()
	if err != nil {
		return err
	}
	signer, err := auth.NewSigner(secret)
	if err != nil {
		return err
	}
	inviteSecret, err := config.InviteSecret()
	if err != nil {
		return err
	}
	invites, err := auth.NewInviteSigner(inviteSecret)
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	// Not a contract endpoint, so not generated and not under /v1.
	mux.Handle("GET /healthz", newHealthHandler(
		checker{name: "postgres", version: pool.ServerVersion},
		checker{name: "redis", version: redis.ServerVersion},
	))
	mux.Handle("/v1/", httpapi.NewRouter(
		httpapi.NewServer(httpapi.Services{
			Auth:    auth.NewService(pool, signer),
			Catalog: catalog.NewService(pool, files, jobsSvc),
			Jobs:    jobsSvc,
			Team:    team.NewService(pool, redis),
			Orders:  orders.NewService(pool, files, jobsSvc),
			Invites: invites,
		}, !config.IsDevelopment()),
		signer,
		httpapi.NewRateLimiter(redis, httpapi.AdminRateLimit, httpapi.AdminRateWindow),
	))

	addr := ":" + config.Getenv("PORT", config.DefaultPort)
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	if config.IsDevelopment() {
		slog.Warn("development mode: signing tokens with the built-in key and issuing non-Secure cookies")
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
