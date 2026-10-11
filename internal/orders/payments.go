package orders

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/db/sqlcgen"
	"github.com/miqbalhamdani/new-commerce-api/internal/midtrans"
	apperrors "github.com/miqbalhamdani/new-commerce-api/internal/platform/errors"
)

// Midtrans payments (P1-220…P1-222; BR-122..BR-126).

// PayLinkTTL is how long a Snap link stays payable (decided 11 Oct 2026).
const PayLinkTTL = 24 * time.Hour

// PayLink is a live Snap link to pay an order at.
type PayLink struct {
	Token, RedirectURL string
	ExpiresAt          time.Time
}

// settingsOf is the shop's storefront settings read through q, or the
// defaults -- bank transfer on, nothing else -- for a shop without a row.
func settingsOf(ctx context.Context, q *sqlcgen.Queries) (sqlcgen.StorefrontSetting, error) {
	st, err := q.GetStorefrontSettings(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return sqlcgen.StorefrontSetting{BankTransferEnabled: true}, nil
	}
	return st, err
}

// takes is whether the shop takes method at checkout (BR-122).
func takes(st sqlcgen.StorefrontSetting, method string) bool {
	switch method {
	case "bank_transfer":
		return st.BankTransferEnabled
	case "midtrans":
		return st.MidtransEnabled && st.MidtransEnvironment != nil && st.MidtransServerKey != nil
	}
	return false
}

// newAttempt writes attempt n of an order's payment before Midtrans is asked
// for its link (03-erd.md §3.6): ERG-000123, then ERG-000123-2 (BR-123). The
// amount is always the order's total; nothing from the client sets it.
func newAttempt(ctx context.Context, q *sqlcgen.Queries, o sqlcgen.Order, n int32) (sqlcgen.Payment, error) {
	id := o.OrderNumber
	if n > 1 {
		id = o.OrderNumber + "-" + strconv.Itoa(int(n))
	}
	return q.CreatePayment(ctx, sqlcgen.CreatePaymentParams{ID: uuid.Must(uuid.NewV7()), OrderID: o.ID,
		ProviderOrderID: id, Attempt: n, GrossAmount: o.TotalAmount, ExpiresAt: time.Now().Add(PayLinkTTL)})
}

// live is whether an attempt's link can still be paid: pending, given a link
// by Midtrans, unexpired, and for the order's total as it is now -- ops may
// have changed shipping on a pending order since (BR-079).
func live(p *sqlcgen.Payment, o sqlcgen.Order) bool {
	return p != nil && p.Status == "pending" && p.SnapToken != nil && p.RedirectUrl != nil &&
		p.ExpiresAt.After(time.Now()) && p.GrossAmount == o.TotalAmount
}

func linkOf(p *sqlcgen.Payment) *PayLink {
	return &PayLink{Token: *p.SnapToken, RedirectURL: *p.RedirectUrl, ExpiresAt: p.ExpiresAt}
}

// openSnap asks Midtrans for an attempt's link and stores it. It runs after
// the attempt's row has committed, and may be slow or fail: the order and
// attempt stand either way, and pay-again can ask again (BR-126).
func (s *Service) openSnap(ctx context.Context, st sqlcgen.StorefrontSetting, o sqlcgen.Order, p sqlcgen.Payment) (*PayLink, error) {
	link, err := s.snap(ctx, st, o, p)
	if err != nil {
		return nil, err
	}
	err = s.tx(ctx, func(q *sqlcgen.Queries, _ pgx.Tx) error {
		return q.SetPaymentLink(ctx, sqlcgen.SetPaymentLinkParams{ID: p.ID, SnapToken: &link.Token, RedirectUrl: &link.RedirectURL})
	})
	if err != nil {
		return nil, err
	}
	return link, nil
}

// snap is the Midtrans call itself, storing nothing: the shop's key opened
// for this one call (BR-129), the attempt's id and amount in whole rupiah.
func (s *Service) snap(ctx context.Context, st sqlcgen.StorefrontSetting, o sqlcgen.Order, p sqlcgen.Payment) (*PayLink, error) {
	if s.Midtrans == nil || s.Box == nil || st.MidtransEnvironment == nil {
		return nil, midtrans.ErrUnavailable
	}
	key, err := s.Box.Open(o.TenantID, st.MidtransServerKey)
	if err != nil {
		return nil, err
	}
	var who struct {
		Name  string  `json:"name"`
		Email *string `json:"email"`
		Phone *string `json:"phone"`
	}
	_ = json.Unmarshal(o.Customer, &who)
	link, err := s.Midtrans.CreateSnap(ctx, *st.MidtransEnvironment, string(key), midtrans.Snap{
		OrderID: p.ProviderOrderID, GrossAmount: p.GrossAmount / 100, // whole rupiah (BR-006)
		Name: who.Name, Email: deref(who.Email), Phone: deref(who.Phone),
		NotificationURL: s.APIURL + "/v1/webhooks/midtrans/" + st.MidtransWebhookID.String(),
		ExpiryHours:     int(PayLinkTTL.Hours()),
	})
	if err != nil {
		// The cause, never the key: it is not in the error to begin with.
		slog.WarnContext(ctx, "midtrans did not open a payment link", "error", err)
		return nil, err
	}
	return &PayLink{Token: link.Token, RedirectURL: link.RedirectURL, ExpiresAt: p.ExpiresAt}, nil
}

// PayAgain is a link to pay a pending Midtrans order (P1-222, BR-126): the
// live one if there is one, else a new attempt for the order's total as it is
// now. The caller has already checked the order is the shopper's.
//
// It holds the order's lock through the Midtrans call, so two taps at once
// get one link. When Midtrans fails, the new attempt still commits: its id
// may already exist at Midtrans, and is never offered twice (BR-123).
func (s *Service) PayAgain(ctx context.Context, orderID uuid.UUID) (*PayLink, error) {
	var (
		link    *PayLink
		snapErr error
	)
	err := s.tx(ctx, func(q *sqlcgen.Queries, _ pgx.Tx) error {
		o, err := q.LockOrder(ctx, orderID)
		if err != nil {
			return notFound(err, "order")
		}
		if o.PaymentMethod != "midtrans" || o.Status != "pending" {
			return fieldError("order_number", "This order has no payment to make: it is paid, cancelled, or not paid through Midtrans.")
		}
		st, err := settingsOf(ctx, q)
		if err != nil {
			return err
		}
		if !takes(st, "midtrans") {
			return fieldError("order_number", "This shop no longer takes payments through Midtrans; contact the shop.")
		}
		var next int32 = 1
		latest, err := q.LatestPayment(ctx, o.ID)
		switch {
		case err == nil && live(&latest, o):
			link = linkOf(&latest)
			return nil
		case err == nil:
			if err := q.FailStalePayment(ctx, latest.ID); err != nil {
				return err
			}
			next = latest.Attempt + 1
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		if err := wholeRupiah(o.TotalAmount); err != nil {
			return err
		}
		p, err := newAttempt(ctx, q, o, next)
		if err != nil {
			return err
		}
		if link, snapErr = s.snap(ctx, st, o, p); snapErr != nil {
			return nil // keep the attempt; answer 502 below
		}
		return q.SetPaymentLink(ctx, sqlcgen.SetPaymentLinkParams{ID: p.ID, SnapToken: &link.Token, RedirectUrl: &link.RedirectURL})
	})
	if err != nil {
		return nil, err
	}
	if snapErr != nil {
		return nil, apperrors.PaymentUnavailable(snapErr)
	}
	return link, nil
}

// wholeRupiah refuses a Midtrans total with sen in it: Midtrans takes whole
// rupiah, so such an order could never be paid (BR-006).
func wholeRupiah(total int64) error {
	if total%100 != 0 {
		return fieldError("payment_method", "This order's total is not a whole rupiah amount, which Midtrans cannot take; choose bank transfer.")
	}
	return nil
}
