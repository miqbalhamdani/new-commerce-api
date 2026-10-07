package auth

import (
	"os"
	"slices"
	"strings"
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

	// 04-api-spec.md 3: v2 makes ops read-only on the catalog. Its one write
	// is orders.
	t.Run("ops reads the catalog and writes only orders", func(t *testing.T) {
		for _, p := range PermissionsFor(RoleOps) {
			if strings.HasSuffix(p, ":write") && p != PermOrdersWrite {
				t.Errorf("ops has %s; v2 gives catalog editing to owner and admin", p)
			}
		}
		if !Can(RoleOps, PermOrdersWrite) {
			t.Error("ops cannot orders:write, and working orders is its job")
		}
	})

	// BR-023: "Read-only. For accountants and external bookkeepers."
	t.Run("viewer writes nothing at all", func(t *testing.T) {
		assertNoWrites(t, RoleViewer)
	})

	// flows.md 6: an ops user gets a 403 on any user-management endpoint.
	t.Run("only owner and admin manage people and keys", func(t *testing.T) {
		for _, p := range []string{PermUsersRead, PermUsersWrite, PermAPIKeysRead, PermAPIKeysWrite} {
			for _, role := range []string{RoleOwner, RoleAdmin} {
				if !Can(role, p) {
					t.Errorf("%s cannot %s", role, p)
				}
			}
			for _, role := range []string{RoleOps, RoleViewer} {
				if Can(role, p) {
					t.Errorf("%s can %s; flows.md 6 says it must not", role, p)
				}
			}
		}
	})

	// Nobody has a reason to be in this system and be unable to see the
	// catalog, so every role reads it.
	t.Run("every role can read the catalog", func(t *testing.T) {
		for _, role := range []string{RoleOwner, RoleAdmin, RoleOps, RoleViewer} {
			for _, p := range readAll {
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

	// Four, matching the CHECK constraint on users.role (03-erd.md 3.2). A
	// fifth here without a migration would be a role nobody can be assigned.
	if len(roles) != 4 {
		t.Fatalf("got %d roles, want 4", len(roles))
	}

	want := []string{RoleOwner, RoleAdmin, RoleOps, RoleViewer}
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
		if strings.HasSuffix(p, ":write") {
			t.Errorf("%s has %s", role, p)
		}
	}
}

// TestMatrixMatchesContract is P1-017's acceptance: the seeded permissions
// equal 04-api-spec.md 3. It reads the table from the pinned contract rather
// than restating it, so bumping the contract without updating the matrix
// fails here.
func TestMatrixMatchesContract(t *testing.T) {
	spec, err := os.ReadFile("../../contracts/04-api-spec.md")
	if err != nil {
		t.Fatalf("read contract: %v (is the submodule checked out?)", err)
	}

	roles := []string{RoleOwner, RoleAdmin, RoleOps, RoleViewer}
	want := map[string][]string{}
	inSection := false
	for line := range strings.Lines(string(spec)) {
		line = strings.TrimRight(line, "\n")
		if strings.HasPrefix(line, "## ") {
			inSection = strings.HasPrefix(line, "## 3. Roles and permissions")
			continue
		}
		if !inSection || !strings.HasPrefix(line, "| `") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		if len(cells) != 1+len(roles) {
			t.Fatalf("matrix row has %d cells, want %d: %s", len(cells), 1+len(roles), line)
		}
		perm := strings.Trim(strings.TrimSpace(cells[0]), "`")
		for i, role := range roles {
			if strings.TrimSpace(cells[i+1]) != "" {
				want[role] = append(want[role], perm)
			}
		}
	}
	if len(want) != len(roles) {
		t.Fatalf("parsed %d roles from the contract, want %d", len(want), len(roles))
	}
	for _, role := range roles {
		slices.Sort(want[role])
		if got := PermissionsFor(role); !slices.Equal(got, want[role]) {
			t.Errorf("%s:\n got  %v\n want %v", role, got, want[role])
		}
	}
}
