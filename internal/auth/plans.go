package auth

import (
	"context"
	"slices"
)

// The three plans (BR-160). They match the CHECK constraint on tenants.plan.
const (
	PlanMulai   = "mulai"
	PlanKatalog = "katalog"
	PlanJualan  = "jualan"
)

var planMulai = []string{
	PermProductsRead, PermProductsWrite, PermVariantsRead, PermVariantsWrite,
	PermCategoriesRead, PermCategoriesWrite, PermBrandsRead, PermBrandsWrite,
	PermMediaRead, PermMediaWrite, PermChannelsRead, PermChannelsWrite,
	PermSettingsRead, PermSettingsWrite, PermAuditLogRead,
}

var planKatalog = concat(planMulai, []string{
	PermBlogRead, PermBlogWrite, PermUsersRead, PermUsersWrite, PermAPIKeysRead, PermAPIKeysWrite,
})

// planPermissions is BR-160's table: what each plan lets any role do at most.
var planPermissions = map[string][]string{
	PlanMulai:   planMulai,
	PlanKatalog: planKatalog,
	PlanJualan: concat(planKatalog, []string{
		PermOrdersRead, PermOrdersWrite, PermCustomersRead, PermCustomersPurge, PermExportsRead,
	}),
}

// PlanSeats is BR-162: a shop's active and invited accounts, owner included.
var PlanSeats = map[string]int{PlanMulai: 1, PlanKatalog: 2, PlanJualan: 5}

// ValidPlan reports whether plan is one of the three.
func ValidPlan(plan string) bool {
	_, ok := planPermissions[plan]
	return ok
}

// PlanAllows reports whether a plan includes a permission. An unknown plan
// allows nothing, as an unknown role does.
func PlanAllows(plan, permission string) bool {
	return slices.Contains(planPermissions[plan], permission)
}

// PermissionsIn is what a role may do in a shop on plan: the role's
// permissions less what the plan leaves out (BR-160), sorted.
func PermissionsIn(role, plan string) []string {
	return slices.DeleteFunc(PermissionsFor(role), func(p string) bool { return !PlanAllows(plan, p) })
}

type planKey struct{}

// NewPlanContext returns a copy of ctx carrying the shop's plan. Set once, by
// the authentication middleware, from the verified access token.
//
// ponytail: a token minted before plans existed has no plan claim and is read
// as jualan, the plan every such shop was migrated to. They expire within 15
// minutes of the deploy, so this branch can go once that has passed.
func NewPlanContext(ctx context.Context, plan string) context.Context {
	if plan == "" {
		plan = PlanJualan
	}
	return context.WithValue(ctx, planKey{}, plan)
}

// Allowed reports whether the caller in ctx may use a permission: their role
// grants it, their shop's plan includes it (BR-160), and it is not erasing a
// shopper's data in an impersonation (BR-152). No role, or no plan, is no.
func Allowed(ctx context.Context, permission string) bool {
	role, _ := RoleFromContext(ctx)
	plan, _ := ctx.Value(planKey{}).(string)
	if permission == PermCustomersPurge && Impersonating(ctx) {
		return false
	}
	return Can(role, permission) && PlanAllows(plan, permission)
}
