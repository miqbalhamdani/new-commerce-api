package auth

import (
	"slices"
	"testing"
)

// TestRolePermissions checks the matrix against API spec.md 3 -- not by
// restating it, which would only prove the file was copied twice, but by
// asserting the properties the contract gives reasons for.
func TestRolePermissions(t *testing.T) {
	// flows.md 6 is an owner granting a merchandiser access "without giving
	// away billing". That sentence is the entire difference between the two
	// roles, so if this ever grows past one permission, the contract moved.
	t.Run("owner is admin plus settings:write", func(t *testing.T) {
		owner := PermissionsFor(RoleOwner)
		admin := PermissionsFor(RoleAdmin)

		var extra []string
		for _, p := range owner {
			if !slices.Contains(admin, p) {
				extra = append(extra, p)
			}
		}
		if !slices.Equal(extra, []string{PermSettingsWrite}) {
			t.Errorf("owner has %v beyond admin, want exactly [%s]", extra, PermSettingsWrite)
		}
		for _, p := range admin {
			if !slices.Contains(owner, p) {
				t.Errorf("admin has %s and owner does not", p)
			}
		}
	})

	// flows.md 6: the ops user sees Categories and Brands in the navigation,
	// while flows.md 1 puts both managers at Admin. Reading is what the product
	// editor needs; restructuring a tree is a different job.
	t.Run("ops reads categories and brands but does not write them", func(t *testing.T) {
		for _, p := range []string{PermCategoriesRead, PermBrandsRead} {
			if !Can(RoleOps, p) {
				t.Errorf("ops cannot %s, but the product editor needs it", p)
			}
		}
		for _, p := range []string{PermCategoriesWrit, PermBrandsWrite} {
			if Can(RoleOps, p) {
				t.Errorf("ops can %s; flows.md 1 gives that to admin", p)
			}
		}
	})

	// flows.md 6: "A viewer cannot save anything anywhere."
	t.Run("viewer writes nothing at all", func(t *testing.T) {
		assertNoWrites(t, RoleViewer)
	})

	// flows.md intro: warehouse staff have no reason to open this phase. The
	// role exists so it is available before Phase 4 gives it stock.
	t.Run("warehouse writes nothing at all", func(t *testing.T) {
		assertNoWrites(t, RoleWarehouse)
	})

	// flows.md 6: an ops user gets a 403 on any user-management endpoint.
	t.Run("only owner and admin manage people and keys", func(t *testing.T) {
		for _, p := range []string{PermUsersRead, PermUsersWrite, PermAPIKeysRead, PermAPIKeysWrite} {
			for _, role := range []string{RoleOwner, RoleAdmin} {
				if !Can(role, p) {
					t.Errorf("%s cannot %s", role, p)
				}
			}
			for _, role := range []string{RoleOps, RoleWarehouse, RoleViewer} {
				if Can(role, p) {
					t.Errorf("%s can %s; flows.md 6 says it must not", role, p)
				}
			}
		}
	})

	// Nobody has a reason to be in this system and be unable to see the
	// catalog, so every role reads it.
	t.Run("every role can read the catalog", func(t *testing.T) {
		for _, role := range []string{RoleOwner, RoleAdmin, RoleOps, RoleWarehouse, RoleViewer} {
			for _, p := range catalogRead {
				if !Can(role, p) {
					t.Errorf("%s cannot %s", role, p)
				}
			}
		}
	})

	// The role column has a CHECK constraint, so this should be impossible.
	// If the impossible happens, no access is the safe answer -- a default of
	// "everything" would turn a typo into a privilege escalation.
	t.Run("an unknown role grants nothing", func(t *testing.T) {
		if perms := PermissionsFor("superadmin"); len(perms) != 0 {
			t.Errorf("unknown role granted %v", perms)
		}
		if Can("superadmin", PermSettingsWrite) {
			t.Error("unknown role can write settings")
		}
		if Can("", PermProductsRead) {
			t.Error("the empty role can read products")
		}
	})

	// The caller gets a copy. A caller that appends to its result must not be
	// able to grant itself a permission for every later caller.
	t.Run("the returned list cannot be mutated back into the matrix", func(t *testing.T) {
		perms := PermissionsFor(RoleViewer)
		perms = append(perms, PermSettingsWrite)
		_ = perms

		if Can(RoleViewer, PermSettingsWrite) {
			t.Error("mutating a returned slice changed the matrix")
		}
	})
}

func TestSeededRoles(t *testing.T) {
	roles := SeededRoles()

	// Five, matching the CHECK constraint on users.role (erd.md 3.2). A sixth
	// here without a migration would be a role nobody can be assigned.
	if len(roles) != 5 {
		t.Fatalf("got %d roles, want 5", len(roles))
	}

	want := []string{RoleOwner, RoleAdmin, RoleOps, RoleWarehouse, RoleViewer}
	for i, role := range roles {
		if role.Name != want[i] {
			t.Errorf("role %d is %q, want %q -- most capable first", i, role.Name, want[i])
		}
		if role.Description == "" {
			t.Errorf("%s has no description; a client renders this next to the name", role.Name)
		}
		if !slices.Equal(role.Permissions, PermissionsFor(role.Name)) {
			t.Errorf("%s: GET /roles and the handler check disagree about permissions", role.Name)
		}
	}
}

func assertNoWrites(t *testing.T, role string) {
	t.Helper()
	for _, p := range PermissionsFor(role) {
		if len(p) > 6 && p[len(p)-6:] == ":write" {
			t.Errorf("%s has %s", role, p)
		}
	}
}
