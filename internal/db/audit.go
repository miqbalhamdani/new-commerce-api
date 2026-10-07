package db

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/db/sqlcgen"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

// AuditEntry is one admin write (BR-018).
type AuditEntry struct {
	Action      string // 'order.transition', 'api_key.create'
	SubjectType string // 'order', 'api_key'
	SubjectID   string
	// Before and After are marshalled to JSON. Record the fields that
	// changed, not the whole row: { "status": "paid" } -> { "status": "processing" }.
	// nil stays NULL -- a create has no before, an archive may have no after.
	Before, After any
}

// Audit records e inside tx, the transaction that makes the change, so the row
// commits with the write and vanishes with its rollback (BR-018).
//
// The actor and IP come from the context the authentication middleware set;
// with no actor (a system job) the row records a NULL actor. The tenant comes
// from the transaction itself.
func Audit(ctx context.Context, tx pgx.Tx, e AuditEntry) error {
	params := sqlcgen.InsertAuditLogParams{
		Action: e.Action, SubjectType: e.SubjectType, SubjectID: e.SubjectID,
	}
	if a, ok := tenant.ActorFromContext(ctx); ok {
		params.ActorID = &a.UserID
		if a.IP.IsValid() {
			params.Ip = &a.IP
		}
	}
	var err error
	if params.Before, err = marshalOrNil(e.Before); err != nil {
		return fmt.Errorf("audit %s: before: %w", e.Action, err)
	}
	if params.After, err = marshalOrNil(e.After); err != nil {
		return fmt.Errorf("audit %s: after: %w", e.Action, err)
	}
	if err := sqlcgen.New(tx).InsertAuditLog(ctx, params); err != nil {
		return fmt.Errorf("audit %s: %w", e.Action, err)
	}
	return nil
}

func marshalOrNil(v any) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	return json.Marshal(v)
}
