package auth

import "context"

// The two business types (BR-170). They match the CHECK constraint on
// tenants.business_type.
const (
	BusinessRetail = "retail"
	BusinessUmrah  = "umrah"
)

// ValidBusinessType reports whether t is one of the two.
func ValidBusinessType(t string) bool { return t == BusinessRetail || t == BusinessUmrah }

type businessTypeKey struct{}

// NewBusinessTypeContext returns a copy of ctx carrying the shop's business
// type. Set once, by the authentication middleware, from the verified access
// token; a token without one is retail, as every shop was before the type.
func NewBusinessTypeContext(ctx context.Context, t string) context.Context {
	if t == "" {
		t = BusinessRetail
	}
	return context.WithValue(ctx, businessTypeKey{}, t)
}

// Umrah reports whether the caller's shop is an umrah travel agency, whose
// products are packages (BR-170).
func Umrah(ctx context.Context) bool {
	t, _ := ctx.Value(businessTypeKey{}).(string)
	return t == BusinessUmrah
}
