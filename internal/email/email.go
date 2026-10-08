// Package email sends the shop's mail through Resend (BR-128).
//
// A service never sends in its request: it enqueues an Envelope once its
// transaction has committed, so a rolled-back change sends nothing, and the
// worker renders and sends it. Envelopes carry ids, not content -- the worker
// reads what it needs and mints any secret (an invitation token) itself, so
// no credential sits in Redis.
package email

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/miqbalhamdani/new-commerce-api/internal/queue"
)

// Stream and Group carry email envelopes; there is no jobs row for them.
const (
	Stream = "emails"
	Group  = "mailers"
)

// Message is one rendered email.
type Message struct {
	FromName string // the shop's name: "{shop}" <no-reply@{domain}>
	To       string
	ReplyTo  string
	Subject  string
	Text     string
	HTML     string
	// Key makes a redelivered send a no-op at Resend.
	Key string
}

// Sender delivers a rendered message.
type Sender interface {
	Send(ctx context.Context, m Message) error
}

// Envelope is what a service enqueues: which email, about whom.
type Envelope struct {
	Kind     string    `json:"kind"` // "invitation"
	TenantID uuid.UUID `json:"tenant_id"`
	UserID   uuid.UUID `json:"user_id"`
	ActorID  uuid.UUID `json:"actor_id"`
}

// Enqueue puts an envelope on the stream. Call it after the transaction that
// caused it has committed.
func Enqueue(ctx context.Context, q *queue.Client, e Envelope) error {
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return q.Add(ctx, Stream, map[string]any{"envelope": string(raw)})
}

// Resend sends through the Resend API.
type Resend struct {
	APIKey string
	Domain string
	Client *http.Client
	URL    string // https://api.resend.com/emails; a test points it elsewhere
}

func (r Resend) Send(ctx context.Context, m Message) error {
	body, _ := json.Marshal(map[string]any{
		"from":     fmt.Sprintf("%q <no-reply@%s>", m.FromName, r.Domain),
		"to":       []string{m.To},
		"reply_to": m.ReplyTo,
		"subject":  m.Subject,
		"text":     m.Text,
		"html":     m.HTML,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+r.APIKey)
	req.Header.Set("Content-Type", "application/json")
	if m.Key != "" {
		req.Header.Set("Idempotency-Key", m.Key)
	}
	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("resend: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode >= 300 {
		return fmt.Errorf("resend: status %d", res.StatusCode)
	}
	return nil
}

// Log stands in for Resend on a developer's machine: it prints the whole
// email, link included, so an invitation can be accepted locally. It writes to
// Out, never to the structured log, and the worker refuses to use it outside
// development -- a deployed log must never carry an invitation token (BR-013).
type Log struct{ Out io.Writer }

func (l Log) Send(_ context.Context, m Message) error {
	_, err := fmt.Fprintf(l.Out, "--- email (not sent: RESEND_API_KEY is unset) ---\nFrom: %q <no-reply@...>\nTo: %s\nReply-To: %s\nSubject: %s\n\n%s---\n",
		m.FromName, m.To, m.ReplyTo, m.Subject, m.Text)
	return err
}

// Render turns an envelope into a message, or nil when there is nothing to
// send any more (the invited user already accepted).
type Render func(ctx context.Context, e Envelope) (*Message, error)

// Consume delivers envelopes until ctx ends. A message that fails to render
// or send is retried after idle; one that keeps failing is dropped after
// maxDeliveries -- a rare lost email is accepted (BR-128).
func Consume(ctx context.Context, q *queue.Client, consumer string, renders map[string]Render, sender Sender, idle time.Duration, maxDeliveries int64) error {
	if err := q.EnsureGroup(ctx, Stream, Group); err != nil {
		return err
	}
	for ctx.Err() == nil {
		msgs, err := q.Reclaim(ctx, Stream, Group, consumer, idle, 10)
		if err == nil && len(msgs) == 0 {
			msgs, err = q.Read(ctx, Stream, Group, consumer, 10, idle/2)
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			slog.WarnContext(ctx, "email stream read failed; retrying", "error", err)
			time.Sleep(time.Second)
			continue
		}
		for _, m := range msgs {
			if err := deliver(ctx, m, renders, sender); err != nil {
				if n, _ := q.Deliveries(ctx, Stream, Group, m.ID); n < maxDeliveries {
					slog.WarnContext(ctx, "email failed; will retry", "error", err)
					continue
				}
				slog.ErrorContext(ctx, "email dropped after retries", "error", err)
			}
			_ = q.Ack(context.WithoutCancel(ctx), Stream, Group, m.ID)
		}
	}
	return nil
}

func deliver(ctx context.Context, m queue.Message, renders map[string]Render, sender Sender) error {
	var e Envelope
	if err := json.Unmarshal([]byte(m.Values["envelope"]), &e); err != nil {
		return nil // malformed: nothing a retry can fix
	}
	render, ok := renders[e.Kind]
	if !ok {
		return nil
	}
	msg, err := render(ctx, e)
	if err != nil || msg == nil {
		return err
	}
	msg.Key = m.ID
	return sender.Send(ctx, *msg)
}
