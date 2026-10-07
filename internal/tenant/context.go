// Package tenant carries the current tenant through a request.
//
// The tenant is derived from the caller's token and nothing else -- never a
// header, a query parameter or a request body (BR-003). Accepting it
// from the request would make cross-tenant access a matter of editing a header.
//
// P1-011's auth middleware is what puts a value in here. Until then the only
// callers are tests.
package tenant

import (
	"context"
	"net/netip"

	"github.com/google/uuid"
)

// contextKey is unexported and of a type declared here, so no other package can
// construct an equal key. A plain string key would let any package -- or any
// dependency -- overwrite the tenant by accident or on purpose.
type contextKey struct{}

var key contextKey

// NewContext returns a copy of ctx carrying id as the current tenant.
func NewContext(ctx context.Context, id uuid.UUID) context.Context {
	return context.WithValue(ctx, key, id)
}

// FromContext returns the tenant carried by ctx.
//
// The boolean is the whole safety property: callers must decide what to do when
// there is no tenant, and db.InTenantTx decides to refuse. There is deliberately
// no variant that returns a zero UUID and lets the caller carry on -- a zero
// tenant matches no rows, which reads as "empty table" rather than as a bug.
func FromContext(ctx context.Context) (uuid.UUID, bool) {
	id, ok := ctx.Value(key).(uuid.UUID)
	return id, ok
}

// Actor is who is acting inside the tenant: the signed-in user and the address
// the request came from. The audit log records both (BR-018); the rate limiter
// keys on the user (BR-014).
type Actor struct {
	UserID uuid.UUID
	IP     netip.Addr // zero when the address could not be parsed
}

type actorKey struct{}

// NewActorContext returns a copy of ctx carrying the acting user, set by the
// authentication middleware from the verified token.
func NewActorContext(ctx context.Context, a Actor) context.Context {
	return context.WithValue(ctx, actorKey{}, a)
}

// ActorFromContext returns the acting user. False means no user: an
// unauthenticated route, or a system job (import worker, retention), which the
// audit log records with a NULL actor.
func ActorFromContext(ctx context.Context) (Actor, bool) {
	a, ok := ctx.Value(actorKey{}).(Actor)
	return a, ok
}
