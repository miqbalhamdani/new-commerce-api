package auth

import (
	"context"
	"slices"
	"testing"
)

// TestPlanPermissions is BR-160's table, row by row.
func TestPlanPermissions(t *testing.T) {
	has := func(plan string, perms ...string) bool {
		for _, p := range perms {
			if !PlanAllows(plan, p) {
				return false
			}
		}
		return true
	}
	catalog := []string{PermProductsWrite, PermVariantsWrite, PermCategoriesWrite, PermBrandsWrite, PermMediaWrite,
		PermChannelsWrite, PermSettingsWrite, PermAuditLogRead}
	katalog := []string{PermBlogWrite, PermUsersWrite, PermAPIKeysWrite}
	sales := []string{PermOrdersRead, PermOrdersWrite, PermCustomersRead, PermCustomersPurge, PermExportsRead}

	for _, tt := range []struct {
		plan                   string
		catalog, katalog, sale bool
	}{
		{PlanMulai, true, false, false},
		{PlanKatalog, true, true, false},
		{PlanJualan, true, true, true},
		{"pintar", false, false, false},
	} {
		if has(tt.plan, catalog...) != tt.catalog || has(tt.plan, katalog...) != tt.katalog || has(tt.plan, sales...) != tt.sale {
			t.Errorf("%s: catalog %v, katalog %v, sales %v; want %v %v %v", tt.plan,
				has(tt.plan, catalog...), has(tt.plan, katalog...), has(tt.plan, sales...), tt.catalog, tt.katalog, tt.sale)
		}
	}
	if !slices.Equal(PermissionsIn(RoleOwner, PlanJualan), PermissionsFor(RoleOwner)) {
		t.Error("jualan takes something away from an owner")
	}
	if got := PermissionsIn(RoleOps, PlanMulai); !slices.Equal(got, []string{PermBrandsRead, PermCategoriesRead,
		PermMediaRead, PermProductsRead, PermVariantsRead}) {
		t.Errorf("ops on mulai: %v", got)
	}
	if slices.Contains(ImpersonationPermissions(PlanJualan), PermCustomersPurge) {
		t.Error("an impersonation may purge")
	}
}

func TestAllowed(t *testing.T) {
	ctx := NewPlanContext(NewRoleContext(context.Background(), RoleOwner), PlanKatalog)
	if !Allowed(ctx, PermBlogWrite) || Allowed(ctx, PermOrdersRead) {
		t.Error("katalog owner")
	}
	if Allowed(NewPlanContext(NewRoleContext(context.Background(), RoleOps), PlanKatalog), PermBlogWrite) {
		t.Error("the plan granted ops what its role does not")
	}
	if !Allowed(NewPlanContext(NewRoleContext(context.Background(), RoleOwner), ""), PermOrdersRead) {
		t.Error("a token from before plans is not jualan")
	}
	imp := NewImpersonationContext(NewPlanContext(NewRoleContext(context.Background(), RoleOwner), PlanJualan))
	if Allowed(imp, PermCustomersPurge) || !Allowed(imp, PermOrdersWrite) {
		t.Error("impersonation")
	}
	if Allowed(NewRoleContext(context.Background(), RoleOwner), PermProductsRead) {
		t.Error("no plan in the context allowed something")
	}
}
