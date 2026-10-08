package httpapi

import (
	"errors"
	"net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/team"
)

// Users: 04-api-spec.md §4 (P1-064); accept-invite §2.

func (s *Server) ListUsers(w http.ResponseWriter, r *http.Request, params ListUsersParams) {
	requirePermission(auth.PermUsersRead, func(w http.ResponseWriter, r *http.Request) {
		var status, role *string
		if params.Status != nil {
			v := string(*params.Status)
			status = &v
		}
		if params.Role != nil {
			v := string(*params.Role)
			role = &v
		}
		var after *team.UserCursor
		if params.Cursor != nil {
			after = &team.UserCursor{}
			if err := decodeCursor(params.Cursor, after); err != nil {
				writeError(w, r, err)
				return
			}
		}
		rows, next, err := s.team.ListUsers(r.Context(), status, role, after, pageLimit(params.Limit))
		if err != nil {
			writeError(w, r, err)
			return
		}
		page := UserPage{Data: make([]User, 0, len(rows))}
		for _, u := range rows {
			page.Data = append(page.Data, userOut(u))
		}
		if next != nil {
			page.NextCursor = encodeCursor(next)
		}
		writeJSON(w, http.StatusOK, page)
	})(w, r)
}

func (s *Server) InviteUser(w http.ResponseWriter, r *http.Request) {
	requirePermission(auth.PermUsersWrite, func(w http.ResponseWriter, r *http.Request) {
		var body InviteUser
		if !decodeJSON(w, r, &body) {
			return
		}
		u, err := s.team.Invite(r.Context(), string(body.Email), body.Name, string(body.Role))
		if err != nil {
			writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusCreated, userOut(u))
	})(w, r)
}

func (s *Server) ResendInvite(w http.ResponseWriter, r *http.Request, id Id) {
	requirePermission(auth.PermUsersWrite, func(w http.ResponseWriter, r *http.Request) {
		if err := s.team.ResendInvite(r.Context(), id); err != nil {
			writeError(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})(w, r)
}

func (s *Server) UpdateUser(w http.ResponseWriter, r *http.Request, id Id) {
	requirePermission(auth.PermUsersWrite, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Role   optional[string] `json:"role"`
			Status optional[string] `json:"status"`
		}
		if !decodeJSON(w, r, &body, "email") {
			return
		}
		if err := rejectNull(map[string]bool{"role": body.Role.Null, "status": body.Status.Null}); err != nil {
			writeError(w, r, err)
			return
		}
		var role, status *string
		if body.Role.Set {
			role = &body.Role.Value
		}
		if body.Status.Set {
			status = &body.Status.Value
		}
		u, err := s.team.Update(r.Context(), id, role, status)
		if err != nil {
			writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, userOut(u))
	})(w, r)
}

func (s *Server) DisableUser(w http.ResponseWriter, r *http.Request, id Id) {
	requirePermission(auth.PermUsersWrite, func(w http.ResponseWriter, r *http.Request) {
		disabled := "disabled"
		if _, err := s.team.Update(r.Context(), id, nil, &disabled); err != nil {
			writeError(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})(w, r)
}

// AcceptInvite handles POST /auth/accept-invite: unauthenticated, the token
// is the credential.
func (s *Server) AcceptInvite(w http.ResponseWriter, r *http.Request) {
	var body AcceptInvite
	if !decodeJSON(w, r, &body) {
		return
	}
	if len(body.Password) < 8 {
		writeError(w, r, fieldErr("password", "password is at least 8 characters"))
		return
	}
	session, err := s.auth.AcceptInvite(r.Context(), s.invites, body.Token, body.Password)
	if errors.Is(err, auth.ErrInvalidInvite) {
		writeError(w, r, fieldErr("token", "This invitation link has expired or was already used. Ask for a new invitation."))
		return
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	s.writeSession(w, r, session)
}

func userOut(u team.User) User {
	return User{Id: u.ID, Email: openapi_types.Email(u.Email), Name: u.Name, Role: UserRole(u.Role), Status: UserStatus(u.Status),
		LastLoginAt: u.LastLoginAt, CreatedAt: u.CreatedAt}
}
