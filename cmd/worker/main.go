// Command worker runs background jobs from the Redis stream (BR-060): image
// derivatives (P1-044), product CSV import (P1-073) and email (P1-226) as
// those items add their handlers.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/miqbalhamdani/new-commerce-api/internal/catalog"
	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/jobs"
	"github.com/miqbalhamdani/new-commerce-api/internal/platform/config"
	"github.com/miqbalhamdani/new-commerce-api/internal/platform/logging"
	"github.com/miqbalhamdani/new-commerce-api/internal/platform/telemetry"
	"github.com/miqbalhamdani/new-commerce-api/internal/queue"
	"github.com/miqbalhamdani/new-commerce-api/internal/storage"
)

func main() {
	slog.SetDefault(logging.New(os.Stderr))
	time.Local = config.WIB
	if err := run(); err != nil {
		slog.Error("worker exited", "error", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	flushTraces, err := telemetry.Setup(ctx,
		config.ServiceName()+"-worker", config.ServiceVersion(), config.Environment(), config.OTLPEndpoint())
	if err != nil {
		return err
	}
	defer func() { _ = flushTraces(context.WithoutCancel(ctx)) }()

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
	catalogSvc := catalog.NewService(pool, files, jobs.NewService(pool, redis))

	host, _ := os.Hostname()
	runner := &jobs.Runner{
		Store: pool,
		Queue: redis,
		Handlers: map[string]jobs.Handler{
			"product_import": catalogSvc.ImportHandler(),
		},
		Consumer:      fmt.Sprintf("%s-%d", host, os.Getpid()),
		ClaimIdle:     60 * time.Second,
		MaxDeliveries: 5,
	}
	slog.Info("worker consuming", "addr", jobs.Stream)
	return runner.Run(ctx)
}
