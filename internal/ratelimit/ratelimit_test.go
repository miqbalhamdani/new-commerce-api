package ratelimit

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

// memCounter is a sliding window that never slides: enough to count.
type memCounter map[string]int

func (m memCounter) SlidingWindow(_ context.Context, key string, limit int, window time.Duration) (bool, int, time.Duration, error) {
	if m[key] >= limit {
		return false, m[key], window, nil
	}
	m[key]++
	return true, m[key], window, nil
}

type brokenCounter struct{}

func (brokenCounter) SlidingWindow(context.Context, string, int, time.Duration) (bool, int, time.Duration, error) {
	return false, 0, 0, errors.New("redis down")
}

// TestCheck: a request passes only if every rule lets it, and the headers
// are the tightest rule's (BR-014).
func TestCheck(t *testing.T) {
	l := New(memCounter{})
	ip, key := Rule{"ip", 3, time.Minute}, Rule{"key", 100, time.Minute}
	for i := 1; i <= 3; i++ {
		res := l.Check(t.Context(), ip, key)
		if !res.Allowed || res.Limit != 3 || res.Remaining != 3-i {
			t.Fatalf("hit %d: %+v, want the IP rule's headers", i, res)
		}
	}
	res := l.Check(t.Context(), ip, key)
	if res.Allowed || res.Limit != 3 || res.Remaining != 0 {
		t.Errorf("over: %+v", res)
	}
	h := http.Header{}
	res.Write(h)
	if h.Get("RateLimit-Limit") != "3" || h.Get("RateLimit-Remaining") != "0" || h.Get("Retry-After") != "60" {
		t.Errorf("headers %v", h)
	}

	// Redis down: each process still counts.
	down := New(brokenCounter{})
	for range 2 {
		if !down.Check(t.Context(), Rule{"k", 2, time.Minute}).Allowed {
			t.Fatal("refused inside the limit with Redis down")
		}
	}
	if down.Check(t.Context(), Rule{"k", 2, time.Minute}).Allowed {
		t.Error("with Redis down, the per-process limit let a third through")
	}
	if rules := Login("shop:1", "a@example.com", "10.0.0.1"); len(rules) != 2 || rules[0].Limit != 10 || rules[1].Limit != 30 {
		t.Errorf("login rules %+v", rules)
	}
}
