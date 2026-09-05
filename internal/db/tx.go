package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

// ErrNoTenantContext is returned when a database call is attempted with no
// tenant in the context.
//
// This is a refusal, not a safety net catching an unsafe path. With no tenant
// set, current_setting('app.tenant_id', true) is NULL, the RLS policy compares
// against NULL, and every row is filtered out -- so nothing leaks either way.
// The problem is what that looks like from the outside: a query that returns
// nothing, on a table that visibly has rows, which someone eventually "fixes"
// by loosening the policy. Failing loudly here keeps that bug findable.
var ErrNoTenantContext = errors.New("no tenant in context")

// InTenantTx runs fn inside a transaction scoped to the context's tenant.
//
// Every query in the application goes through this. The tenant is set per
// transaction, never per connection: pgx pools connections, so a session-level
// SET would outlive the request and follow the connection into the next
// request -- serving one tenant's rows to another.
//
//	err := store.InTenantTx(ctx, func(tx pgx.Tx) error {
//	    return q.WithTx(tx).CreateProduct(ctx, params)
//	})
func (s *Store) InTenantTx(ctx context.Context, fn func(pgx.Tx) error) error {
	tenantID, ok := tenant.FromContext(ctx)
	if !ok {
		// Before BeginTx on purpose: no connection is taken from the pool and
		// fn never runs.
		return ErrNoTenantContext
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	// A no-op once Commit has succeeded. WithoutCancel so that a request whose
	// context has already expired still releases the transaction rather than
	// leaving the connection to be torn down.
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	// is_local = true scopes the setting to this transaction, so COMMIT or
	// ROLLBACK clears it. That is the line that makes pooling safe.
	if _, err := tx.Exec(ctx,
		`SELECT set_config('app.tenant_id', $1::text, true)`, tenantID.String()); err != nil {
		return fmt.Errorf("set tenant context: %w", err)
	}

	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}
