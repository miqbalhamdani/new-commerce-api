package orders

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/db/sqlcgen"
	"github.com/miqbalhamdani/new-commerce-api/internal/email"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

// Order emails (P1-228, BR-128): placed, payment received, shipped. Each is
// queued after the change that caused it has committed, once per change --
// a move to the status an order already has is no change, so it sends
// nothing. The worker renders it, and skips an order with no email.

// queueMail queues kind for an order. A queue that fails costs one email,
// never the change the shopper or ops just made: it has committed.
func (s *Service) queueMail(ctx context.Context, kind string, tenantID, orderID uuid.UUID) {
	if s.Mail == nil {
		return
	}
	if err := s.Mail(ctx, email.Envelope{Kind: kind, TenantID: tenantID, OrderID: orderID}); err != nil {
		slog.WarnContext(ctx, "order email not queued", "error", err)
	}
}

// mailFor is the email a move to status sends, if any.
func mailFor(status string) string {
	switch status {
	case "paid":
		return "payment_received"
	case "shipped":
		return "order_shipped"
	}
	return ""
}

// OrderMail renders an order email in the worker (BR-128): from the shop's
// name, Reply-To its contact email, keyed by order and event so Resend sends
// one however often it is queued.
func OrderMail(store *db.Store) email.Render {
	return func(ctx context.Context, e email.Envelope) (*email.Message, error) {
		tctx := tenant.NewContext(ctx, e.TenantID)
		var (
			o     sqlcgen.Order
			lines []sqlcgen.OrderLine
			shop  sqlcgen.Tenant
			st    sqlcgen.StorefrontSetting
		)
		err := store.InTenantTx(tctx, func(tx pgx.Tx) (err error) {
			q := sqlcgen.New(tx)
			if o, err = q.GetOrder(tctx, e.OrderID); err != nil {
				return err
			}
			if lines, err = q.GetOrderLines(tctx, o.ID); err != nil {
				return err
			}
			if st, err = settingsOf(tctx, q); err != nil {
				return err
			}
			shop, err = q.GetTenant(tctx, e.TenantID)
			return err
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		var who struct {
			Name  string  `json:"name"`
			Email *string `json:"email"`
		}
		_ = json.Unmarshal(o.Customer, &who)
		if who.Email == nil || strings.TrimSpace(*who.Email) == "" {
			return nil, nil // nobody to tell
		}

		var subject, text, html string
		switch e.Kind {
		case "order_placed":
			ls := make([]email.OrderLine, 0, len(lines))
			for _, l := range lines {
				ls = append(ls, email.OrderLine{Title: l.TitleSnapshot, Qty: int(l.Qty),
					Total: Rupiah(l.UnitPrice*int64(l.Qty) - l.DiscountAmount)})
			}
			instructions := ""
			if o.PaymentMethod == "bank_transfer" && st.BankTransferInstructions != nil {
				instructions = *st.BankTransferInstructions
			}
			subject, text, html = email.OrderPlaced(shop.Name, who.Name, o.OrderNumber, ls, Rupiah(o.TotalAmount), instructions)
		case "payment_received":
			subject, text, html = email.PaymentReceived(shop.Name, who.Name, o.OrderNumber, Rupiah(o.TotalAmount))
		case "order_shipped":
			subject, text, html = email.OrderShipped(shop.Name, who.Name, o.OrderNumber, strings.ToUpper(deref(o.Courier)), deref(o.TrackingNumber))
		default:
			return nil, nil
		}
		msg := &email.Message{FromName: shop.Name, To: *who.Email, Subject: subject, Text: text, HTML: html,
			Key: o.ID.String() + ":" + e.Kind}
		if st.ContactEmail != nil {
			msg.ReplyTo = *st.ContactEmail
		}
		return msg, nil
	}
}

// Rupiah is minor units the way an Indonesian reads them: Rp 199.000, with
// sen only when there are any (BR-006).
func Rupiah(minor int64) string {
	neg := minor < 0
	if neg {
		minor = -minor
	}
	whole, sen := strconv.FormatInt(minor/100, 10), minor%100
	var b strings.Builder
	for i, c := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(c)
	}
	out := "Rp " + b.String()
	if sen != 0 {
		out += "," + strconv.FormatInt(100+sen, 10)[1:]
	}
	if neg {
		out = "-" + out
	}
	return out
}
