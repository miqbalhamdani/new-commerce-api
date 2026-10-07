package db

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/miqbalhamdani/new-commerce-api/internal/platform/config"
)

// TestWIB is P1-081's acceptance at the pool: every connection runs in
// Asia/Jakarta, and a timestamptz leaves as JSON with +07:00 even when the
// process itself runs in UTC (BR-007).
func TestWIB(t *testing.T) {
	ctx := t.Context()
	store, err := New(ctx, config.AppDatabaseURL())
	if err != nil {
		t.Fatalf("connect as app_user: %v", err)
	}
	t.Cleanup(store.Close)

	saved := time.Local
	time.Local = time.UTC
	t.Cleanup(func() { time.Local = saved })

	var zone string
	if err := store.pool.QueryRow(ctx, `SHOW TimeZone`).Scan(&zone); err != nil {
		t.Fatalf("show timezone: %v", err)
	}
	if zone != "Asia/Jakarta" {
		t.Errorf("session TimeZone %q, want Asia/Jakarta", zone)
	}

	var now time.Time
	if err := store.pool.QueryRow(ctx, `SELECT now()`).Scan(&now); err != nil {
		t.Fatalf("select now(): %v", err)
	}
	out, err := json.Marshal(now)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.HasSuffix(strings.Trim(string(out), `"`), "+07:00") {
		t.Errorf("now() marshals as %s, want a +07:00 offset", out)
	}
}
