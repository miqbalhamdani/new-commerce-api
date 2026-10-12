// Package biteship quotes shipping (BR-120, BR-121): Biteship's rates API with
// the one platform key, a fixed-rate stand-in for development without one, and
// a 10-minute cache in front of either. No booking, labels or pickup.
package biteship

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ErrUnavailable is Biteship not answering, or answering with an error: the
// caller's 502 shipping_rates_unavailable (BR-121).
var ErrUnavailable = errors.New("shipping rates unavailable")

// Request is one parcel to quote: from the shop's origin to a postal code,
// for the couriers it offers. Value is in minor units, like every amount in
// the system.
type Request struct {
	Origin, Destination string
	Couriers            []string
	WeightGrams         int64
	Value               int64
}

// Option is one courier service and its price in minor units (BR-006).
type Option struct {
	CourierCode, ServiceCode string
	CourierName, ServiceName string
	Price                    int64
	Duration                 string
}

// Quoter quotes a parcel.
type Quoter interface {
	Rates(ctx context.Context, r Request) ([]Option, error)
}

// Client is Biteship's rates API, POST /v1/rates/couriers.
type Client struct {
	Key  string
	URL  string // https://api.biteship.com; a test points it at a fake
	HTTP *http.Client
}

// NewClient is a Client with a timeout: a quote is on checkout's path.
//
// ponytail: one 5 s timeout, no retry. A retry belongs here if Biteship turns
// out to drop requests rather than be down.
func NewClient(key, url string) Client {
	return Client{Key: key, URL: url, HTTP: &http.Client{Timeout: 5 * time.Second}}
}

func (c Client) Rates(ctx context.Context, r Request) ([]Option, error) {
	origin, err1 := strconv.Atoi(r.Origin)
	dest, err2 := strconv.Atoi(r.Destination)
	if err := errors.Join(err1, err2); err != nil {
		return nil, fmt.Errorf("postal codes: %w", err)
	}
	body, _ := json.Marshal(map[string]any{
		"origin_postal_code": origin, "destination_postal_code": dest,
		"couriers": strings.Join(r.Couriers, ","),
		// One parcel: the quote is for the total weight (BR-121). Biteship
		// takes whole rupiah and at least a gram.
		"items": []map[string]any{{"name": "Order", "value": r.Value / 100,
			"weight": max(r.WeightGrams, 1), "quantity": 1}},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.URL, "/")+"/v1/rates/couriers", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", c.Key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		Success bool   `json:"success"`
		Error   string `json:"error"`
		Pricing []struct {
			CourierCode        string `json:"courier_code"`
			CourierName        string `json:"courier_name"`
			CourierServiceCode string `json:"courier_service_code"`
			CourierServiceName string `json:"courier_service_name"`
			Duration           string `json:"duration"`
			Price              int64  `json:"price"`
		} `json:"pricing"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || resp.StatusCode/100 != 2 || !out.Success {
		return nil, fmt.Errorf("%w: status %d %s %v", ErrUnavailable, resp.StatusCode, out.Error, err)
	}
	opts := make([]Option, 0, len(out.Pricing))
	for _, p := range out.Pricing {
		opts = append(opts, Option{CourierCode: p.CourierCode, ServiceCode: p.CourierServiceCode,
			CourierName: p.CourierName, ServiceName: p.CourierServiceName, Price: p.Price * 100, Duration: p.Duration})
	}
	slices.SortStableFunc(opts, func(a, b Option) int { return cmp.Compare(a.Price, b.Price) })
	return opts, nil
}

// Fixed is development's stand-in when there is no Biteship key: every
// courier offers one Reguler service at Rp 10.000, so checkout works locally.
// It never runs anywhere else: cmd/api refuses to start without a key there.
type Fixed struct{}

func (Fixed) Rates(_ context.Context, r Request) ([]Option, error) {
	opts := make([]Option, 0, len(r.Couriers))
	for _, c := range r.Couriers {
		opts = append(opts, Option{CourierCode: c, ServiceCode: "reg", CourierName: strings.ToUpper(c),
			ServiceName: "Reguler", Price: 1000000, Duration: "2 - 3 days"})
	}
	return opts, nil
}

// Cache is where quotes are kept: Redis in production.
type Cache interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
}

// QuoteTTL is how long a quote holds: checkout inside it charges what the
// shopper was shown (BR-121).
const QuoteTTL = 10 * time.Minute

// Cached quotes through a cache keyed per shop, origin, destination, total
// weight and couriers (BR-121). A cache that fails is skipped, not fatal:
// a live quote is still a quote.
type Cached struct {
	Quoter Quoter
	Cache  Cache
}

// Quote is the shop's options for r, cheapest first. A shop that has not set
// its origin or couriers has none.
func (c Cached) Quote(ctx context.Context, tenantID uuid.UUID, r Request) ([]Option, error) {
	if r.Origin == "" || len(r.Couriers) == 0 {
		return []Option{}, nil
	}
	couriers := slices.Clone(r.Couriers)
	slices.Sort(couriers)
	key := fmt.Sprintf("rates:%s:%s:%s:%d:%s", tenantID, r.Origin, r.Destination, r.WeightGrams, strings.Join(couriers, ","))
	if raw, err := c.Cache.Get(ctx, key); err == nil && raw != nil {
		var opts []Option
		if json.Unmarshal(raw, &opts) == nil {
			return opts, nil
		}
	}
	opts, err := c.Quoter.Rates(ctx, r)
	if err != nil {
		return nil, err
	}
	if raw, err := json.Marshal(opts); err == nil {
		_ = c.Cache.Set(ctx, key, raw, QuoteTTL)
	}
	return opts, nil
}
