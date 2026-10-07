// Package queue owns the connection to Redis.
//
// Phase 1 uses it for little more than a liveness check; the Redis Streams job
// runner arrives in P1-060. It exists now so that when it does, nothing else
// has grown its own client.
package queue

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// Client is a Redis connection.
type Client struct {
	rdb *redis.Client
}

// New dials Redis and verifies it answers before returning.
func New(ctx context.Context, url string) (*Client, error) {
	opt, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("parse redis url: %w", err)
	}
	rdb := redis.NewClient(opt)
	if err := rdb.Ping(ctx).Err(); err != nil {
		_ = rdb.Close()
		return nil, fmt.Errorf("ping redis: %w", err)
	}
	return &Client{rdb: rdb}, nil
}

// Close releases the connection.
func (c *Client) Close() error { return c.rdb.Close() }

// slidingWindow is a sliding-window log (BR-014): one sorted-set member per
// accepted hit, scored by its time in ms. A rejected hit is not recorded, so a
// caller hammering past the limit does not push its own reset further away.
// Atomic as a script; a pipeline would let two replicas both see limit-1.
var slidingWindow = redis.NewScript(`
local key, now, window, limit = KEYS[1], tonumber(ARGV[1]), tonumber(ARGV[2]), tonumber(ARGV[3])
redis.call('ZREMRANGEBYSCORE', key, '-inf', now - window)
local count = redis.call('ZCARD', key)
local allowed = 0
if count < limit then
  redis.call('ZADD', key, now, ARGV[4])
  count = count + 1
  allowed = 1
end
redis.call('PEXPIRE', key, window)
local oldest = redis.call('ZRANGE', key, 0, 0, 'WITHSCORES')
return {allowed, count, tonumber(oldest[2] or now)}
`)

// SlidingWindow records one hit against key unless limit hits already fall
// inside the trailing window. It reports whether the hit was allowed, how many
// hits the window now holds, and how long until the oldest one leaves it.
func (c *Client) SlidingWindow(ctx context.Context, key string, limit int, window time.Duration) (bool, int, time.Duration, error) {
	now := time.Now()
	res, err := slidingWindow.Run(ctx, c.rdb, []string{key},
		now.UnixMilli(), window.Milliseconds(), limit, uuid.NewString()).Int64Slice()
	if err != nil {
		return false, 0, 0, fmt.Errorf("redis sliding window: %w", err)
	}
	resetIn := time.UnixMilli(res[2]).Add(window).Sub(now)
	return res[0] == 1, int(res[1]), resetIn, nil
}

// ServerVersion reports the redis_version field of INFO server, e.g. "8.4.0".
func (c *Client) ServerVersion(ctx context.Context) (string, error) {
	info, err := c.rdb.Info(ctx, "server").Result()
	if err != nil {
		return "", fmt.Errorf("redis INFO server: %w", err)
	}
	for line := range strings.SplitSeq(info, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "redis_version:"); ok {
			return v, nil
		}
	}
	return "", fmt.Errorf("redis INFO server: no redis_version field in response")
}
