package queue

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// Message is one stream entry delivered to a consumer.
type Message struct {
	ID     string
	Values map[string]string
}

// EnsureGroup creates the consumer group (and the stream) if missing.
func (c *Client) EnsureGroup(ctx context.Context, stream, group string) error {
	err := c.rdb.XGroupCreateMkStream(ctx, stream, group, "0").Err()
	if err != nil && !strings.Contains(err.Error(), "BUSYGROUP") {
		return fmt.Errorf("create group %s/%s: %w", stream, group, err)
	}
	return nil
}

// Add appends a message to a stream.
func (c *Client) Add(ctx context.Context, stream string, values map[string]any) error {
	if err := c.rdb.XAdd(ctx, &redis.XAddArgs{Stream: stream, Values: values}).Err(); err != nil {
		return fmt.Errorf("xadd %s: %w", stream, err)
	}
	return nil
}

// Read returns up to count new messages for this consumer, waiting up to
// block for one to arrive. Nothing new is an empty slice, not an error.
func (c *Client) Read(ctx context.Context, stream, group, consumer string, count int64, block time.Duration) ([]Message, error) {
	res, err := c.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{Group: group, Consumer: consumer,
		Streams: []string{stream, ">"}, Count: count, Block: block}).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("xreadgroup %s: %w", stream, err)
	}
	var out []Message
	for _, s := range res {
		out = append(out, toMessages(s.Messages)...)
	}
	return out, nil
}

// Reclaim takes over messages another consumer has held longer than idle --
// the consumer died mid-job -- so they are delivered again (at-least-once,
// BR-060).
func (c *Client) Reclaim(ctx context.Context, stream, group, consumer string, idle time.Duration, count int64) ([]Message, error) {
	msgs, _, err := c.rdb.XAutoClaim(ctx, &redis.XAutoClaimArgs{Stream: stream, Group: group,
		Consumer: consumer, MinIdle: idle, Start: "0-0", Count: count}).Result()
	if err != nil {
		return nil, fmt.Errorf("xautoclaim %s: %w", stream, err)
	}
	return toMessages(msgs), nil
}

// Deliveries is how many times a pending message has been delivered.
func (c *Client) Deliveries(ctx context.Context, stream, group, id string) (int64, error) {
	p, err := c.rdb.XPendingExt(ctx, &redis.XPendingExtArgs{Stream: stream, Group: group,
		Start: id, End: id, Count: 1}).Result()
	if err != nil {
		return 0, fmt.Errorf("xpending %s: %w", stream, err)
	}
	if len(p) == 0 {
		return 0, nil
	}
	return p[0].RetryCount, nil
}

// Ack marks a message handled; it is never delivered again.
func (c *Client) Ack(ctx context.Context, stream, group, id string) error {
	if err := c.rdb.XAck(ctx, stream, group, id).Err(); err != nil {
		return fmt.Errorf("xack %s: %w", stream, err)
	}
	return nil
}

func toMessages(in []redis.XMessage) []Message {
	out := make([]Message, 0, len(in))
	for _, m := range in {
		vals := make(map[string]string, len(m.Values))
		for k, v := range m.Values {
			vals[k] = fmt.Sprint(v)
		}
		out = append(out, Message{ID: m.ID, Values: vals})
	}
	return out
}
