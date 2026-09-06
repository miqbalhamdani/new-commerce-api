package auth

import "slices"

// The five seeded roles. Phase 1 has no custom roles and no per-user overrides:
// a permission is granted by the role and by nothing else.
//
// These match the CHECK constraint on users.role (erd.md 3.2). Adding a sixth
// means a migration, a contract change, and a look at every client that renders
// a role picker.
const (
	RoleOwner     = "owner"
	RoleAdmin     = "admin"
	RoleOps       = "ops"
	RoleWarehouse = "warehouse"
	RoleViewer    = "viewer"
)

// Permissions are resource:action. Two actions only -- read and write. A third
// for deletion would be a distinction without a difference here, where deleting
// is archiving and is part of writing.
const (
	PermProductsRead   = "products:read"
	PermProductsWrite  = "products:write"
	PermVariantsRead   = "variants:read"
	PermVariantsWrite  = "variants:write"
	PermCategoriesRead = "categories:read"
	PermCategoriesWrit = "categories:write"
	PermBrandsRead     = "brands:read"
	PermBrandsWrite    = "brands:write"
	PermMediaRead      = "media:read"
	PermMediaWrite     = "media:write"
	PermExportsRead    = "exports:read"
	PermUsersRead      = "users:read"
	PermUsersWrite     = "users:write"
	PermAPIKeysRead    = "api_keys:read"
	PermAPIKeysWrite   = "api_keys:write"
	PermSettingsRead   = "settings:read"
	PermSettingsWrite  = "settings:write"
)

// catalogRead is what every role can do: see the catalog. Nobody has a reason
// to be in this system and be unable to read it.
var catalogRead = []string{
	PermProductsRead, PermVariantsRead, PermCategoriesRead,
	PermBrandsRead, PermMediaRead, PermSettingsRead,
}

// rolePermissions is API spec.md 3, in code. It is the only definition -- the
// handler check, the login response and GET /roles all read from here, so they
// cannot disagree.
var rolePermissions = map[string][]string{
	// Everything an admin can do, plus the tenant's own settings. That single
	// permission is the whole difference: flows.md 6 is an owner granting a
	// merchandiser access "without giving away billing".
	RoleOwner: concat(adminPermissions, []string{PermSettingsWrite}),

	RoleAdmin: adminPermissions,

	// Works in the catalog daily. Reads categories and brands -- the product
	// editor needs that to file a product -- but restructuring either tree is
	// a different job, and flows.md 1 gives it to admin.
	RoleOps: concat(catalogRead, []string{
		PermProductsWrite, PermVariantsWrite, PermMediaWrite, PermExportsRead,
	}),

	// No reason to open this phase at all (flows.md intro). It exists so the
	// role is available before Phase 4 gives it stock.
	RoleWarehouse: catalogRead,

	// Writes nothing, anywhere. A client must not render a save control for
	// this role -- absent, not disabled.
	RoleViewer: concat(catalogRead, []string{PermExportsRead}),
}

var adminPermissions = concat(catalogRead, []string{
	PermProductsWrite, PermVariantsWrite, PermCategoriesWrit, PermBrandsWrite,
	PermMediaWrite, PermExportsRead,
	PermUsersRead, PermUsersWrite, PermAPIKeysRead, PermAPIKeysWrite,
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

// SeededRole is one row of API spec.md 3, for GET /roles.
type SeededRole struct {
	Name        string
	Description string
	Permissions []string
}

// SeededRoles returns the five roles in the order a client should show them:
// most capable first, which is also the order they appear in the contract.
func SeededRoles() []SeededRole {
	order := []struct{ name, description string }{
		{RoleOwner, "Full access, including the tenant's own settings."},
		{RoleAdmin, "Everything operational, plus managing people and API keys."},
		{RoleOps, "Works in the catalog daily. Cannot restructure categories or brands."},
		{RoleWarehouse, "Read-only. Gains stock permissions in a later phase."},
		{RoleViewer, "Read-only. Cannot save anything, anywhere."},
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
