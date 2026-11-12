package auth

import "context"

// roleKey is unexported and of a type declared here, so no other package can
// construct an equal key and put a role of its own choosing into a request.
type roleKey struct{}

// NewRoleContext returns a copy of ctx carrying the caller's role.
//
// Set once, by the authentication middleware, from the verified access token.
// Never from anything a client sends directly.
func NewRoleContext(ctx context.Context, role string) context.Context {
	return context.WithValue(ctx, roleKey{}, role)
}

// RoleFromContext returns the caller's role.
//
// The boolean distinguishes "no role" from "a role with no permissions". The
// first means the request was never authenticated and should be a 401; the
// second is a real 403.
func RoleFromContext(ctx context.Context) (string, bool) {
	role, ok := ctx.Value(roleKey{}).(string)
	return role, ok
}

type impersonatingKey struct{}

// NewImpersonationContext marks the request as a platform admin working in
// someone else's shop (BR-152). Set only by the authentication middleware,
// from a verified token's imp claim.
func NewImpersonationContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, impersonatingKey{}, true)
}

// Impersonating reports whether the request is an impersonation (BR-152).
func Impersonating(ctx context.Context) bool {
	v, _ := ctx.Value(impersonatingKey{}).(bool)
	return v
}
