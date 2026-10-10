package queue

import (
	"testing"

	"github.com/miqbalhamdani/new-commerce-api/internal/platform/config"
)

func TestZZProbe(t *testing.T) {
	c, err := New(t.Context(), config.RedisURL())
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	consumers, err := c.rdb.XInfoConsumers(ctx, "jobs", "workers").Result()
	if err != nil {
		t.Fatalf("xinfo consumers: %v", err)
	}
	for _, co := range consumers {
		t.Logf("consumer %q pending=%d idle=%s inactive=%s", co.Name, co.Pending, co.Idle, co.Inactive)
	}
	n, _ := c.rdb.XLen(ctx, "jobs").Result()
	p, err := c.rdb.XPending(ctx, "jobs", "workers").Result()
	if err != nil {
		t.Fatalf("xpending: %v", err)
	}
	t.Logf("stream len=%d pending count=%d lower=%s higher=%s perConsumer=%v", n, p.Count, p.Lower, p.Higher, p.Consumers)
}
