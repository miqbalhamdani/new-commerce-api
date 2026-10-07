package auth

import "slices"

// The four seeded roles (BR-023). There are no custom roles and no per-user
// overrides: a permission is granted by the role and by nothing else.
//
// These match the CHECK constraint on users.role (03-erd.md 3.2). Adding a
// fifth means a migration, a contract change, and a look at every client that
// renders a role picker. v1's warehouse is gone with the stock module (BR-017).
const (
	RoleOwner  = "owner"
	RoleAdmin  = "admin"
	RoleOps    = "ops"
	RoleViewer = "viewer"
)

// Permissions are resource:action. Two actions only -- read and write. A third
// for deletion would be a distinction without a difference here, where deleting
// is archiving and is part of writing.
const (
	PermProductsRead    = "products:read"
	PermProductsWrite   = "products:write"
	PermVariantsRead    = "variants:read"
	PermVariantsWrite   = "variants:write"
	PermCategoriesRead  = "categories:read"
	PermCategoriesWrite = "categories:write"
	PermBrandsRead      = "brands:read"
	PermBrandsWrite     = "brands:write"
	PermMediaRead       = "media:read"
	PermMediaWrite      = "media:write"
	PermOrdersRead      = "orders:read"
	PermOrdersWrite     = "orders:write"
	PermCustomersRead   = "customers:read"
	PermExportsRead     = "exports:read"
	PermChannelsRead    = "channels:read"
	PermChannelsWrite   = "channels:write"
	PermUsersRead       = "users:read"
	PermUsersWrite      = "users:write"
	PermAPIKeysRead     = "api_keys:read"
	PermAPIKeysWrite    = "api_keys:write"
	PermAuditLogRead    = "audit_log:read"
	PermSettingsRead    = "settings:read"
	PermSettingsWrite   = "settings:write"
)

// readAll is what every role can do: see the catalog, the orders and the
// customers. Nobody has a reason to be in this system and be unable to.
// Settings are not in it: ops does not read them (04-api-spec.md 3, BR-025).
var readAll = []string{
	PermProductsRead, PermVariantsRead, PermCategoriesRead, PermBrandsRead,
	PermMediaRead, PermOrdersRead, PermCustomersRead, PermExportsRead,
}

// rolePermissions is 04-api-spec.md 3, in code. It is the only definition --
// the handler check, the login response and GET /roles all read from here, so
// they cannot disagree.
var rolePermissions = map[string][]string{
	// Everything an admin can do, plus the tenant's own settings, and nothing
	// more (04-api-spec.md 3).
	RoleOwner: concat(adminPermissions, []string{PermSettingsWrite}),

	RoleAdmin: adminPermissions,

	// Works orders and customers; reads the catalog. v2 took catalog writes
	// away from ops -- editing it belongs to owner and admin -- and it has no
	// Settings screen, so no settings:read either.
	RoleOps: concat(readAll, []string{PermOrdersWrite}),

	// Writes nothing, anywhere. A client must not render a save control for
	// this role -- absent, not disabled (BR-025). Reads channels, which ops
	// does not: a bookkeeper reconciles marketplace listings, ops does not.
	RoleViewer: concat(readAll, []string{PermChannelsRead, PermSettingsRead}),
}

var adminPermissions = concat(readAll, []string{
	PermProductsWrite, PermVariantsWrite, PermCategoriesWrite, PermBrandsWrite,
	PermMediaWrite, PermOrdersWrite, PermChannelsRead, PermChannelsWrite,
	PermUsersRead, PermUsersWrite, PermAPIKeysRead, PermAPIKeysWrite,
	PermAuditLogRead, PermSettingsRead,
})

// PermissionsFor returns everything a role grants.
//
// An unknown role gets nothing rather than everything. The role column has a
// CHECK constraint, so an unknown value should be impossible -- and if the
// impossible happens, the safe answer is no access.
func PermissionsFor(role string) []string {
	perms, ok := rolePermissions[role]
	if !ok {
		return []string{}
	}
	out := make([]string, len(perms))
	copy(out, perms)
	slices.Sort(out)
	return out
}

// Can reports whether a role grants a permission.
func Can(role, permission string) bool {
	return slices.Contains(rolePermissions[role], permission)
}

// SeededRole is one column of 04-api-spec.md 3, for GET /roles.
type SeededRole struct {
	Name        string
	Description string
	Permissions []string
}

// SeededRoles returns the four roles in the order a client should show them:
// most capable first, which is also the order they appear in the contract.
func SeededRoles() []SeededRole {
	order := []struct{ name, description string }{
		{RoleOwner, "Everything, including the tenant's own settings."},
		{RoleAdmin, "Everything except the tenant's settings and billing."},
		{RoleOps, "Works orders and customers. Reads the catalog."},
		{RoleViewer, "Read-only. For accountants and external bookkeepers."},
	}

	roles := make([]SeededRole, 0, len(order))
	for _, r := range order {
		roles = append(roles, SeededRole{
			Name:        r.name,
			Description: r.description,
			Permissions: PermissionsFor(r.name),
		})
	}
	return roles
}

func concat(lists ...[]string) []string {
	var out []string
	for _, l := range lists {
		out = append(out, l...)
	}
	return out
}
