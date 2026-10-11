package orders

import (
	"context"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"math/big"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/db/sqlcgen"
	"github.com/miqbalhamdani/new-commerce-api/internal/midtrans"
	apperrors "github.com/miqbalhamdani/new-commerce-api/internal/platform/errors"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

// Midtrans' notification webhook (P1-221, 04-api-spec.md §11.10; BR-003,
// BR-124, BR-125).

// badSignature is a notification whose signature_key does not verify with the
// shop's server key: 401, and nothing changes.
func badSignature() error {
	return apperrors.Unauthenticated("The notification's signature does not verify.")
}

// Notify handles one Midtrans notification for the shop webhookID names.
//
// The body is Midtrans', so it is read as it comes -- extra fields kept, none
// refused -- and believed for nothing: the webhook id says whose it might be,
// the signature with that shop's key says it is, and Get Status says what
// actually happened (BR-124). Only then is anything written, in one
// transaction with the order's status (BR-125).
func (s *Service) Notify(ctx context.Context, webhookID uuid.UUID, raw []byte) error {
	var n struct {
		OrderID      string `json:"order_id"`
		StatusCode   string `json:"status_code"`
		GrossAmount  string `json:"gross_amount"`
		SignatureKey string `json:"signature_key"`
	}
	if err := json.Unmarshal(raw, &n); err != nil {
		return badSignature()
	}
	hook, err := s.store.ResolveMidtransWebhook(ctx, webhookID)
	if errors.Is(err, db.ErrNotFound) {
		return apperrors.NotFound("No such webhook.")
	}
	if err != nil {
		return err
	}
	if s.Midtrans == nil || s.Box == nil || hook.Environment == nil {
		return apperrors.Internal(errors.New("midtrans is not configured"))
	}
	key, err := s.Box.Open(hook.TenantID, hook.ServerKey)
	if err != nil {
		return apperrors.Internal(err)
	}
	sum := sha512.Sum512([]byte(n.OrderID + n.StatusCode + n.GrossAmount + string(key)))
	if subtle.ConstantTimeCompare([]byte(hex.EncodeToString(sum[:])), []byte(n.SignatureKey)) != 1 {
		return badSignature()
	}

	// Verified: the shop is the webhook's, and from here on the tenant. An
	// order id that is not one of its attempts is not ours to ask about or
	// change; it is answered 200 like the rest.
	ctx = tenant.NewContext(ctx, hook.TenantID)
	err = s.tx(ctx, func(q *sqlcgen.Queries, _ pgx.Tx) error {
		_, err := q.PaymentByProviderOrderID(ctx, n.OrderID)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		slog.InfoContext(ctx, "midtrans notification for an unknown order id")
		return nil
	}
	if err != nil {
		return err
	}
	st, err := s.Midtrans.GetStatus(ctx, *hook.Environment, string(key), n.OrderID)
	if err != nil {
		// 503, so Midtrans retries and the retry finishes the job.
		return &apperrors.Error{Code: "payment_unavailable", Status: 503, Title: "Payment status unavailable",
			Detail: "Midtrans did not confirm the payment; send the notification again."}
	}
	return s.apply(ctx, n.OrderID, st, raw)
}

// apply records what Get Status confirmed about one attempt (BR-125).
func (s *Service) apply(ctx context.Context, providerOrderID string, st midtrans.Status, raw []byte) error {
	return s.tx(ctx, func(q *sqlcgen.Queries, tx pgx.Tx) error {
		found, err := q.PaymentByProviderOrderID(ctx, providerOrderID)
		if err != nil {
			return err
		}
		// The order before the attempt, as pay-again locks them.
		o, err := q.LockOrder(ctx, found.OrderID)
		if err != nil {
			return err
		}
		p, err := q.LockPayment(ctx, found.ID)
		if err != nil {
			return err
		}

		next := sqlcgen.RecordNotificationParams{ID: p.ID, Status: p.Status, TransactionStatus: &st.TransactionStatus,
			LastNotification: raw, PaidAt: p.PaidAt}
		settled := st.TransactionStatus == "settlement" || st.TransactionStatus == "capture" && st.FraudStatus == "accept"
		switch {
		case p.Status == "paid":
			// Already paid: a replay, or a later status that cannot unpay it.
		case settled && (!sameAmount(st.GrossAmount, p.GrossAmount) || p.GrossAmount != o.TotalAmount):
			// Flagged for an operator; never pays (BR-124).
			next.Status = "amount_mismatch"
		case settled:
			// Even an attempt already failed: an old link paid in another
			// tab is still money received.
			now := time.Now()
			next.Status, next.PaidAt = "paid", &now
		case st.TransactionStatus == "expire" || st.TransactionStatus == "cancel" || st.TransactionStatus == "deny":
			if p.Status == "pending" {
				next.Status = "failed"
			}
		}
		if err := q.RecordNotification(ctx, next); err != nil {
			return err
		}
		if next.Status != "paid" || p.Status == "paid" {
			return nil
		}

		// The money arrived. A pending order is now paid (BR-074); one ops
		// already moved on stays where it is; a cancelled one owes a refund,
		// and the audit log says so for whoever handles refunds.
		switch o.Status {
		case "pending":
			_, _, err = transition(ctx, q, tx, o.ID, "paid", TransitionInput{})
			return err
		case "cancelled":
			return db.Audit(ctx, tx, db.AuditEntry{Action: "payment.after_cancel", SubjectType: "order",
				SubjectID: o.ID.String(), After: map[string]any{"provider_order_id": p.ProviderOrderID, "refund_owed": true}})
		}
		return nil
	})
}

// sameAmount is Midtrans' gross_amount, whole rupiah as a decimal string
// ("409000.00"), against an attempt's amount in minor units (BR-006).
func sameAmount(gross string, minor int64) bool {
	r, ok := new(big.Rat).SetString(gross)
	if !ok {
		return false
	}
	return r.Mul(r, big.NewRat(100, 1)).Cmp(big.NewRat(minor, 1)) == 0
}
