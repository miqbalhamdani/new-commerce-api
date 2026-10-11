// Package midtrans talks to Midtrans for the shop that owns the payment: each
// shop has its own account, keys and environment (BR-122, BR-123). It holds no
// state and touches no database; the server key arrives already opened and is
// never logged (BR-013, BR-129).
package midtrans

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ErrUnavailable is Midtrans not answering, or refusing: the caller's
// payment_unavailable (BR-126).
var ErrUnavailable = errors.New("midtrans unavailable")

// Hosts are one environment's base URLs: Snap, the hosted payment page, and
// the core API that answers Get Status (BR-124).
type Hosts struct{ Snap, API string }

// Environments are Midtrans' two; a test points a Client at a fake instead.
var Environments = map[string]Hosts{
	"sandbox":    {Snap: "https://app.sandbox.midtrans.com", API: "https://api.sandbox.midtrans.com"},
	"production": {Snap: "https://app.midtrans.com", API: "https://api.midtrans.com"},
}

// SnapJS is the script a website loads to open Snap for environment.
func SnapJS(environment string) string { return Environments[environment].Snap + "/snap/snap.js" }

// Client calls Midtrans.
type Client struct {
	Hosts map[string]Hosts
	HTTP  *http.Client
}

// NewClient is a Client for the real environments.
//
// ponytail: one 10 s timeout, no retry; the shopper is waiting at checkout.
func NewClient() *Client {
	return &Client{Hosts: Environments, HTTP: &http.Client{Timeout: 10 * time.Second}}
}

// Snap is one transaction to open: the order (or attempt) id, its total in
// whole rupiah (BR-006), who pays, where Midtrans reports back (BR-124) and
// how long the link lasts.
type Snap struct {
	OrderID         string
	GrossAmount     int64
	Name, Email     string
	Phone           string
	NotificationURL string
	ExpiryHours     int
}

// Link is a Snap transaction's token and the page to pay on.
type Link struct {
	Token, RedirectURL string
}

// CreateSnap opens a Snap transaction.
func (c *Client) CreateSnap(ctx context.Context, environment, serverKey string, s Snap) (Link, error) {
	h, ok := c.Hosts[environment]
	if !ok {
		return Link{}, fmt.Errorf("%w: no environment %q", ErrUnavailable, environment)
	}
	body, _ := json.Marshal(map[string]any{
		"transaction_details": map[string]any{"order_id": s.OrderID, "gross_amount": s.GrossAmount},
		"customer_details":    map[string]any{"first_name": s.Name, "email": s.Email, "phone": s.Phone},
		"expiry":              map[string]any{"unit": "hours", "duration": s.ExpiryHours},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.Snap+"/snap/v1/transactions", bytes.NewReader(body))
	if err != nil {
		return Link{}, err
	}
	authorize(req, serverKey)
	// Every transaction names its own notification URL, so the owner never
	// has to configure one in the Midtrans dashboard (BR-124).
	req.Header.Set("X-Override-Notification", s.NotificationURL)
	var out struct {
		Token       string   `json:"token"`
		RedirectURL string   `json:"redirect_url"`
		Errors      []string `json:"error_messages"`
	}
	if err := c.do(req, &out); err != nil {
		return Link{}, err
	}
	if out.Token == "" {
		return Link{}, fmt.Errorf("%w: %s", ErrUnavailable, strings.Join(out.Errors, "; "))
	}
	return Link{Token: out.Token, RedirectURL: out.RedirectURL}, nil
}

// Status is what the Get Status API says about a transaction: the one answer
// the webhook trusts (BR-124).
type Status struct {
	StatusCode        string `json:"status_code"`
	TransactionStatus string `json:"transaction_status"`
	FraudStatus       string `json:"fraud_status"`
	GrossAmount       string `json:"gross_amount"`
	OrderID           string `json:"order_id"`
}

// GetStatus asks Midtrans about orderID.
func (c *Client) GetStatus(ctx context.Context, environment, serverKey, orderID string) (Status, error) {
	h, ok := c.Hosts[environment]
	if !ok {
		return Status{}, fmt.Errorf("%w: no environment %q", ErrUnavailable, environment)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.API+"/v2/"+orderID+"/status", nil)
	if err != nil {
		return Status{}, err
	}
	authorize(req, serverKey)
	var st Status
	if err := c.do(req, &st); err != nil {
		return Status{}, err
	}
	if st.TransactionStatus == "" {
		return Status{}, fmt.Errorf("%w: status %s", ErrUnavailable, st.StatusCode)
	}
	return st, nil
}

// authorize is Midtrans' HTTP Basic: the server key as user, no password.
func authorize(req *http.Request, serverKey string) {
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(serverKey+":")))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
}

func (c *Client) do(req *http.Request, out any) error {
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 500 {
		return fmt.Errorf("%w: status %d", ErrUnavailable, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return nil
}
