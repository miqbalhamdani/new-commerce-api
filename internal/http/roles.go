package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
)

// ListRoles handles GET /roles.
//
// A constant: Phase 1 has no custom roles and no per-user overrides, so this is
// the same for every tenant. It exists so a client can render a role picker and
// explain what each role means without hardcoding the matrix.
//
// It needs users:read because it is part of the team-management surface. That
// is what makes an ops user's 403 here the exact case flows.md 6 describes.
func (s *Server) ListRoles(w http.ResponseWriter, r *http.Request) {
	requirePermission(auth.PermUsersRead, s.listRoles)(w, r)
}

func (s *Server) listRoles(w http.ResponseWriter, _ *http.Request) {
	seeded := auth.SeededRoles()

	body := make([]Role, 0, len(seeded))
	for _, role := range seeded {
		body = append(body, Role{
			Name:        RoleName(role.Name),
			Description: &role.Description,
			Permissions: role.Permissions,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}
