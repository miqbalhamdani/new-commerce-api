package biteship

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// fakeBiteship answers /v1/rates/couriers the way Biteship does, and records
// what it was asked.
func fakeBiteship(t *testing.T, status int, body string) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	var asked []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/rates/couriers" || r.Header.Get("Authorization") != "test-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		asked = append(asked, req)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &asked
}

const pricing = `{"success": true, "pricing": [
  {"courier_code": "sicepat", "courier_name": "SiCepat", "courier_service_code": "reg", "courier_service_name": "Reguler", "duration": "1 - 2 days", "price": 10000},
  {"courier_code": "jne", "courier_name": "JNE", "courier_service_code": "reg", "courier_service_name": "Reguler", "duration": "2 - 3 days", "price": 9000}]}`

// TestClient: Biteship is asked in whole rupiah for one parcel, and answers
// in minor units, cheapest first (BR-006, BR-120).
func TestClient(t *testing.T) {
	srv, asked := fakeBiteship(t, 200, pricing)
	c := NewClient("test-key", srv.URL)
	opts, err := c.Rates(t.Context(), Request{Origin: "40115", Destination: "60231", Couriers: []string{"jne", "sicepat"},
		WeightGrams: 400, Value: 29800000})
	if err != nil || len(opts) != 2 {
		t.Fatalf("rates: %v %v", opts, err)
	}
	if opts[0].CourierCode != "jne" || opts[0].Price != 900000 || opts[1].Price != 1000000 || opts[0].Duration != "2 - 3 days" {
		t.Errorf("options %+v: want minor units, cheapest first", opts)
	}
	req := (*asked)[0]
	item := req["items"].([]any)[0].(map[string]any)
	if req["origin_postal_code"] != float64(40115) || req["couriers"] != "jne,sicepat" || item["weight"] != float64(400) || item["value"] != float64(298000) {
		t.Errorf("asked %v", req)
	}

	for what, fake := range map[string]struct {
		status int
		body   string
	}{
		"a 500":         {500, `{"success": false, "error": "down"}`},
		"success false": {200, `{"success": false, "error": "no couriers"}`},
		"not json":      {200, `<html>`},
	} {
		srv, _ := fakeBiteship(t, fake.status, fake.body)
		if _, err := NewClient("test-key", srv.URL).Rates(t.Context(), Request{Origin: "40115", Destination: "60231", Couriers: []string{"jne"}}); !errors.Is(err, ErrUnavailable) {
			t.Errorf("%s: %v, want ErrUnavailable", what, err)
		}
	}
	if _, err := NewClient("test-key", "http://127.0.0.1:1").Rates(t.Context(), Request{Origin: "40115", Destination: "60231"}); !errors.Is(err, ErrUnavailable) {
		t.Errorf("unreachable: %v", err)
	}
}

type memCache struct {
	mu   sync.Mutex
	m    map[string][]byte
	fail bool
}

func (c *memCache) Get(_ context.Context, k string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fail {
		return nil, errors.New("redis down")
	}
	return c.m[k], nil
}

func (c *memCache) Set(_ context.Context, k string, v []byte, _ time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fail {
		return errors.New("redis down")
	}
	c.m[k] = v
	return nil
}

// TestCached: a quote is reused for the same shop, route, weight and
// couriers (BR-121), and a broken cache costs a live quote, not a failure.
func TestCached(t *testing.T) {
	srv, asked := fakeBiteship(t, 200, pricing)
	cache := &memCache{m: map[string][]byte{}}
	c := Cached{Quoter: NewClient("test-key", srv.URL), Cache: cache}
	shop := uuid.New()
	r := Request{Origin: "40115", Destination: "60231", Couriers: []string{"sicepat", "jne"}, WeightGrams: 400}

	first, _ := c.Quote(t.Context(), shop, r)
	r.Couriers = []string{"jne", "sicepat"} // same set, another order
	second, err := c.Quote(t.Context(), shop, r)
	if err != nil || len(*asked) != 1 || second[0].Price != first[0].Price {
		t.Fatalf("second quote asked Biteship again (%d asks) or changed: %v %v", len(*asked), second, err)
	}
	for what, other := range map[string]func(Request) Request{
		"another weight": func(r Request) Request { r.WeightGrams = 401; return r },
		"another place":  func(r Request) Request { r.Destination = "10110"; return r },
	} {
		before := len(*asked)
		_, _ = c.Quote(t.Context(), shop, other(r))
		if len(*asked) != before+1 {
			t.Errorf("%s came from the cache", what)
		}
	}
	if _, _ = c.Quote(t.Context(), uuid.New(), r); len(*asked) != 4 {
		t.Error("another shop's quote came from this shop's cache")
	}

	cache.fail = true
	if opts, err := c.Quote(t.Context(), shop, r); err != nil || len(opts) != 2 {
		t.Errorf("with the cache down: %v %v", opts, err)
	}
	if opts, err := c.Quote(t.Context(), shop, Request{Destination: "60231", Couriers: []string{"jne"}}); err != nil || len(opts) != 0 {
		t.Errorf("no origin: %v %v, want no options", opts, err)
	}
}
