package jobs_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/jobs"
	"github.com/miqbalhamdani/new-commerce-api/internal/platform/config"
	"github.com/miqbalhamdani/new-commerce-api/internal/queue"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

// TestKilledJobIsRedeliveredAndFinishesOnce is P1-060's acceptance: a worker
// killed mid-run leaves the job for another, which finishes it; a further
// delivery of the same message does nothing (BR-060).
func TestKilledJobIsRedeliveredAndFinishesOnce(t *testing.T) {
	ctx := t.Context()
	store, redis := deps(t)
	tenantID := seedTenant(ctx, t, store)
	tctx := tenant.NewContext(ctx, tenantID)
	svc := jobs.NewService(store, redis)

	var job jobs.Job
	if err := store.InTenantTx(tctx, func(tx pgx.Tx) (err error) {
		job, err = jobs.Create(tctx, tx, "product_import", map[string]string{"r2_key": "x"}, nil)
		return err
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := svc.Enqueue(ctx, job); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	started := make(chan struct{}, 4)
	var finished atomic.Int32
	handler := func(ctx context.Context, j jobs.Job, _ jobs.Progress) (any, error) {
		started <- struct{}{}
		select {
		case <-ctx.Done(): // the first worker is killed here
			return nil, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
		finished.Add(1)
		return map[string]int{"created": 1}, nil
	}
	runner := func(name string) *jobs.Runner {
		return &jobs.Runner{Store: store, Queue: redis, Handlers: map[string]jobs.Handler{"product_import": handler},
			Consumer: name, ClaimIdle: 300 * time.Millisecond, MaxDeliveries: 5}
	}

	// Worker one picks the job up and dies mid-run.
	ctx1, kill := context.WithCancel(ctx)
	done1 := make(chan struct{})
	go func() { _ = runner("killed-" + job.ID.String()).Run(ctx1); close(done1) }()
	waitFor(t, started)
	kill()
	<-done1
	if s := state(t, store, tctx, job.ID); s != "running" {
		t.Fatalf("after the kill the job is %s, want running (unacknowledged)", s)
	}

	// Worker two reclaims it once it has been idle long enough, and finishes.
	ctx2, stop2 := context.WithCancel(ctx)
	defer stop2()
	go func() { _ = runner("rescuer-" + job.ID.String()).Run(ctx2) }()
	deadline := time.Now().Add(5 * time.Second)
	for state(t, store, tctx, job.ID) != "done" {
		if time.Now().After(deadline) {
			t.Fatal("the reclaimed job never finished")
		}
		time.Sleep(50 * time.Millisecond)
	}

	// The same message delivered again (a duplicate XADD) is skipped.
	if err := svc.Enqueue(ctx, job); err != nil {
		t.Fatalf("re-enqueue: %v", err)
	}
	time.Sleep(500 * time.Millisecond)
	if n := finished.Load(); n != 1 {
		t.Errorf("handler finished %d times, want 1", n)
	}
	got, err := svc.Get(tctx, job.ID)
	if err != nil || got.State != "done" || got.FinishedAt == nil || string(got.Result) != `{"created": 1}` {
		t.Errorf("job %+v %v", got, err)
	}
}

// TestFailingJobGivesUp: a job that keeps failing is marked failed with a
// Problem after MaxDeliveries, and is not retried after that.
func TestFailingJobGivesUp(t *testing.T) {
	ctx := t.Context()
	store, redis := deps(t)
	tctx := tenant.NewContext(ctx, seedTenant(ctx, t, store))
	svc := jobs.NewService(store, redis)

	var job jobs.Job
	if err := store.InTenantTx(tctx, func(tx pgx.Tx) (err error) {
		job, err = jobs.Create(tctx, tx, "product_import", nil, nil)
		return err
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = svc.Enqueue(ctx, job)

	var calls atomic.Int32
	r := &jobs.Runner{Store: store, Queue: redis, Consumer: "failing-" + job.ID.String(),
		ClaimIdle: 100 * time.Millisecond, MaxDeliveries: 3,
		Handlers: map[string]jobs.Handler{"product_import": func(context.Context, jobs.Job, jobs.Progress) (any, error) {
			calls.Add(1)
			return nil, errors.New("boom")
		}}}
	rctx, stop := context.WithCancel(ctx)
	defer stop()
	go func() { _ = r.Run(rctx) }()

	deadline := time.Now().Add(5 * time.Second)
	for state(t, store, tctx, job.ID) != "failed" {
		if time.Now().After(deadline) {
			t.Fatalf("never failed; %d calls", calls.Load())
		}
		time.Sleep(50 * time.Millisecond)
	}
	got, _ := svc.Get(tctx, job.ID)
	if calls.Load() != 3 || got.Error == nil {
		t.Errorf("%d calls, error %s", calls.Load(), got.Error)
	}
}

func deps(t *testing.T) (*db.Store, *queue.Client) {
	t.Helper()
	store, err := db.New(t.Context(), config.AppDatabaseURL())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(store.Close)
	redis, err := queue.New(t.Context(), config.RedisURL())
	if err != nil {
		t.Fatalf("redis: %v", err)
	}
	t.Cleanup(func() { _ = redis.Close() })
	return store, redis
}

func seedTenant(ctx context.Context, t *testing.T, store *db.Store) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	if err := store.InTenantTx(tenant.NewContext(ctx, id), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO tenants (id, name, slug, order_prefix) VALUES ($1, 'Jobs', $2, 'JOB')`,
			id, "jobs-"+id.String())
		return err
	}); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		bg := tenant.NewContext(context.WithoutCancel(ctx), id)
		_ = store.InTenantTx(bg, func(tx pgx.Tx) error {
			_, err := tx.Exec(bg, `DELETE FROM jobs; DELETE FROM tenants WHERE id = $1`, id)
			return err
		})
	})
	return id
}

func state(t *testing.T, store *db.Store, ctx context.Context, id uuid.UUID) string {
	t.Helper()
	var s string
	if err := store.InTenantTx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT state FROM jobs WHERE id = $1`, id).Scan(&s)
	}); err != nil {
		t.Fatalf("state: %v", err)
	}
	return s
}

func waitFor(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("the handler never started")
	}
}
