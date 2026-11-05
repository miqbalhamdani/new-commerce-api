package platformadmin

import (
	"testing"
	"time"

	"github.com/miqbalhamdani/new-commerce-api/internal/db"
)

// TestCount is BR-150's summary: only self-signed-up shops count, and
// sleeping is verified with no activity in 14 days.
func TestCount(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	ago := func(d int) *time.Time { v := now.AddDate(0, 0, -d); return &v }
	active := &Owner{Status: "active"}
	shops := []Shop{
		// New, verified, busy: a product, an order, a set-up website.
		{Source: SourceEmail, CreatedAt: *ago(2), Owner: active, LastActivityAt: ago(1),
			Website: Website{SetUp: true}, Stats: db.ShopStats{ProductsTotal: 3, OrdersTotal: 1}},
		// A month old, verified, quiet for 20 days: sleeping.
		{Source: SourceGoogle, CreatedAt: *ago(25), Owner: active, LastActivityAt: ago(20)},
		// Never confirmed the email, never active: not sleeping, just unverified.
		{Source: SourceEmail, CreatedAt: *ago(40), Owner: &Owner{Status: "invited"}},
		// Made by demo-seed: listed, not counted.
		{Source: SourceCLI, CreatedAt: *ago(1), Owner: active, LastActivityAt: ago(0),
			Stats: db.ShopStats{ProductsTotal: 50, OrdersTotal: 9}},
	}
	var got Summary
	for _, s := range shops {
		count(&got, s, now)
	}
	want := Summary{Shops: 3, New7d: 1, New30d: 2, Verified: 2, WebsiteSetUp: 1, WithProducts: 1, WithOrders: 1,
		Active7d: 1, Sleeping: 1}
	if got != want {
		t.Errorf("summary\n got %+v\nwant %+v", got, want)
	}
}
