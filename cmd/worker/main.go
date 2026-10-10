// Command worker runs background jobs from the Redis stream (BR-060): image
// derivatives (P1-044), product CSV import (P1-073), and email (P1-226) from
// its own stream.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/catalog"
	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/email"
	"github.com/miqbalhamdani/new-commerce-api/internal/images"
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
	jobsSvc := jobs.NewService(pool, redis)
	catalogSvc := catalog.NewService(pool, files, jobsSvc)
	ordersSvc := orders.NewService(pool, files, jobsSvc)

	host, _ := os.Hostname()
	runner := &jobs.Runner{
		Store: pool,
		Queue: redis,
		Handlers: map[string]jobs.Handler{
			"product_import":    catalogSvc.ImportHandler(),
			"order_export":      ordersSvc.ExportHandler(),
			"image_derivatives": images.Handler(pool, files),
		},
		Consumer:      fmt.Sprintf("%s-%d", host, os.Getpid()),
		ClaimIdle:     60 * time.Second,
		MaxDeliveries: 5,
	}
	sender, err := mailSender()
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
	renders := map[string]email.Render{"invitation": team.InvitationMail(pool, invites, config.AdminURL())}
	go func() {
		if err := email.Consume(ctx, redis, runner.Consumer, renders, sender, 60*time.Second, 5); err != nil {
			slog.Error("email consumer stopped", "error", err)
		}
	}()

	slog.Info("worker consuming", "addr", jobs.Stream)
	return runner.Run(ctx)
}

// mailSender is Resend when a key is set. Without one, development sends to a
// local SMTP inbox (Mailpit) when SMTP_ADDR is set and otherwise prints mail to
// stdout; anywhere else that is a startup failure, since printed mail would put
// invitation tokens in a deployed log (BR-013).
func mailSender() (email.Sender, error) {
	if key := config.ResendAPIKey(); key != "" {
		return email.Resend{APIKey: key, Domain: config.EmailDomain(), URL: "https://api.resend.com/emails"}, nil
	}
	if config.IsDevelopment() && config.SMTPAddr() != "" {
		return email.SMTP{Addr: config.SMTPAddr(), Domain: config.EmailDomain()}, nil
	}
	if config.IsDevelopment() {
		return email.Log{Out: os.Stdout}, nil
	}
	return nil, errors.New("RESEND_API_KEY is required when ENVIRONMENT is not \"development\"")
}
