// Package jobs runs long work in the background (BR-060): the API creates a
// row and enqueues it on a Redis stream; cmd/worker delivers it to a handler
// and records the outcome on the row, which is what GET /v1/jobs/{id} reads.
//
// Delivery is at-least-once. A worker that dies mid-job leaves its message
// pending; another reclaims it after ClaimIdle. A row already done or failed
// is skipped, so a redelivery finishes a job once -- and every handler must
// still be safe to run twice.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/db/sqlcgen"
	apperrors "github.com/miqbalhamdani/new-commerce-api/internal/platform/errors"
	"github.com/miqbalhamdani/new-commerce-api/internal/queue"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

// The stream and consumer group every job travels on (03-erd.md §3.2).
const (
	Stream = "jobs"
	Group  = "workers"
)

type Job = sqlcgen.Job

// Create inserts a queued job inside tx, the transaction that asked for it.
// Enqueue it once that transaction has committed.
func Create(ctx context.Context, tx pgx.Tx, kind string, params any, createdBy *uuid.UUID) (Job, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return Job{}, err
	}
	return sqlcgen.New(tx).CreateJob(ctx, sqlcgen.CreateJobParams{
		ID: uuid.Must(uuid.NewV7()), Kind: kind, Params: raw, CreatedBy: createdBy})
}

// Service reads jobs and enqueues committed ones.
type Service struct {
	store *db.Store
	queue *queue.Client
}

func NewService(store *db.Store, q *queue.Client) *Service { return &Service{store: store, queue: q} }

// Enqueue puts a committed job on the stream.
//
// ponytail: a crash between commit and XADD strands the job as queued; a
// sweeper re-enqueueing old queued rows closes that if it is ever seen.
func (s *Service) Enqueue(ctx context.Context, job Job) error {
	return s.queue.Add(ctx, Stream, map[string]any{
		"job_id": job.ID.String(), "tenant_id": job.TenantID.String(), "kind": job.Kind})
}

// Get reads a job in the caller's tenant.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (Job, error) {
	var j Job
	err := s.store.InTenantTx(ctx, func(tx pgx.Tx) (err error) {
		j, err = sqlcgen.New(tx).GetJob(ctx, id)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, apperrors.NotFound("No such job.")
	}
	return j, err
}

// Progress reports how far a handler is. total may be nil while unknown.
type Progress func(processed int, total *int, failed int) error

// Handler does one job and returns what goes in its result. It runs in the
// job's tenant with no actor, and must be safe to run twice (BR-060).
type Handler func(ctx context.Context, job Job, progress Progress) (result any, err error)

// Runner consumes the stream.
type Runner struct {
	Store         *db.Store
	Queue         *queue.Client
	Handlers      map[string]Handler
	Consumer      string        // unique per worker process
	ClaimIdle     time.Duration // how long a message may sit with a dead consumer
	MaxDeliveries int64         // the delivery that fails at this count fails the job
}

// Run consumes until ctx ends.
func (r *Runner) Run(ctx context.Context) error {
	if err := r.Queue.EnsureGroup(ctx, Stream, Group); err != nil {
		return err
	}
	lastClaim := time.Time{}
	for ctx.Err() == nil {
		var msgs []queue.Message
		var err error
		if time.Since(lastClaim) >= r.ClaimIdle/2 {
			lastClaim = time.Now()
			msgs, err = r.Queue.Reclaim(ctx, Stream, Group, r.Consumer, r.ClaimIdle, 10)
		}
		if err == nil && len(msgs) == 0 {
			msgs, err = r.Queue.Read(ctx, Stream, Group, r.Consumer, 10, r.ClaimIdle/2)
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			slog.WarnContext(ctx, "job stream read failed; retrying", "error", err)
			time.Sleep(time.Second)
			continue
		}
		for _, m := range msgs {
			r.process(ctx, m)
		}
	}
	return nil
}

// process runs one message. It acknowledges only once the row records an
// outcome; a worker killed before that leaves the message for reclaiming.
func (r *Runner) process(ctx context.Context, m queue.Message) {
	jobID, err1 := uuid.Parse(m.Values["job_id"])
	tenantID, err2 := uuid.Parse(m.Values["tenant_id"])
	if err1 != nil || err2 != nil {
		slog.ErrorContext(ctx, "malformed job message; dropping", "error", errors.Join(err1, err2))
		_ = r.Queue.Ack(ctx, Stream, Group, m.ID)
		return
	}
	tctx := tenant.NewContext(ctx, tenantID)
	ack := func() {
		if err := r.Queue.Ack(context.WithoutCancel(ctx), Stream, Group, m.ID); err != nil {
			slog.ErrorContext(ctx, "ack job", "error", err)
		}
	}

	var job Job
	err := r.Store.InTenantTx(tctx, func(tx pgx.Tx) (err error) {
		job, err = sqlcgen.New(tx).StartJob(tctx, jobID)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) { // done, failed or gone: nothing left to do
		ack()
		return
	}
	if err != nil {
		slog.ErrorContext(ctx, "start job", "error", err)
		return
	}

	handler, ok := r.Handlers[job.Kind]
	if !ok {
		r.fail(tctx, jobID, fmt.Errorf("no handler for %s", job.Kind))
		ack()
		return
	}
	result, err := handler(tctx, job, r.progress(tctx, jobID))
	if ctx.Err() != nil {
		return // killed mid-run: the message stays pending and is reclaimed
	}
	if err != nil {
		n, derr := r.Queue.Deliveries(ctx, Stream, Group, m.ID)
		if derr == nil && n < r.MaxDeliveries {
			slog.WarnContext(ctx, "job failed; will retry", "error", err)
			return
		}
		r.fail(tctx, jobID, err)
		ack()
		return
	}
	var raw []byte
	if result != nil {
		if raw, err = json.Marshal(result); err != nil {
			r.fail(tctx, jobID, err)
			ack()
			return
		}
	}
	if err := r.Store.InTenantTx(tctx, func(tx pgx.Tx) error {
		return sqlcgen.New(tx).FinishJob(tctx, sqlcgen.FinishJobParams{ID: jobID, Result: raw})
	}); err != nil {
		slog.ErrorContext(ctx, "finish job", "error", err)
		return
	}
	ack()
}

func (r *Runner) progress(ctx context.Context, id uuid.UUID) Progress {
	return func(processed int, total *int, failed int) error {
		var t *int32
		if total != nil {
			v := int32(*total)
			t = &v
		}
		return r.Store.InTenantTx(ctx, func(tx pgx.Tx) error {
			return sqlcgen.New(tx).JobProgress(ctx, sqlcgen.JobProgressParams{ID: id,
				Processed: int32(processed), Total: t, Failed: int32(failed)})
		})
	}
}

// fail records a Problem on the row. The cause is logged; the client sees a
// fixed message, as with any internal error.
func (r *Runner) fail(ctx context.Context, id uuid.UUID, cause error) {
	slog.ErrorContext(ctx, "job failed", "error", cause)
	problem := apperrors.From(cause)
	raw, _ := json.Marshal(map[string]any{"type": "https://docs.example.com/errors/" + problem.Code,
		"title": problem.Title, "status": problem.Status, "detail": problem.Detail, "trace_id": apperrors.TraceID(ctx)})
	if err := r.Store.InTenantTx(ctx, func(tx pgx.Tx) error {
		return sqlcgen.New(tx).FailJob(ctx, sqlcgen.FailJobParams{ID: id, Error: raw})
	}); err != nil {
		slog.ErrorContext(ctx, "record job failure", "error", err)
	}
}
