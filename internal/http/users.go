package httpapi

import (
	"errors"
	"net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	apperrors "github.com/miqbalhamdani/new-commerce-api/internal/platform/errors"
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
			Bio    optional[string] `json:"bio"`
		}
		if !decodeJSON(w, r, &body, "email") {
			return
		}
		if err := rejectNull(map[string]bool{"role": body.Role.Null, "status": body.Status.Null}); err != nil {
			writeError(w, r, err)
			return
		}
		var role, status, bio *string
		if body.Role.Set {
			role = &body.Role.Value
		}
		if body.Status.Set {
			status = &body.Status.Value
		}
		if body.Bio.Set {
			bio = &body.Bio.Value // "" when null: both clear it (BR-133)
		}
		u, err := s.team.Update(r.Context(), id, role, status, bio)
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
		if _, err := s.team.Update(r.Context(), id, nil, &disabled, nil); err != nil {
			writeError(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})(w, r)
}

// Register handles POST /auth/register (P1-084): unauthenticated, and 202
// whatever the email, so it says nothing about who has an account (BR-021).
func (s *Server) Register(w http.ResponseWriter, r *http.Request) {
	var body RegisterRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	// Counted like a sign-in, before anything touches the database (BR-014).
	if s.limits != nil && !s.limits.checkLogin(w, r, "signup", string(body.Email)) {
		return
	}
	if err := s.team.Register(r.Context(), body.ShopName, body.Name, string(body.Email), clientIP(r)); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// VerifyCode handles POST /auth/verify-code (P1-086): the emailed code buys
// the invitation token its link carries; the password comes next, through
// accept-invite (BR-026).
func (s *Server) VerifyCode(w http.ResponseWriter, r *http.Request) {
	var body VerifyCodeRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	// Counted before the code is checked: ten guesses per email per 15
	// minutes is what makes six digits enough (BR-014).
	if s.limits != nil && !s.limits.checkLogin(w, r, "code", string(body.Email)) {
		return
	}
	token, err := s.auth.VerifyCode(r.Context(), s.invites, string(body.Email), body.Code)
	if errors.Is(err, auth.ErrInvalidCode) {
		writeError(w, r, fieldErr("code", "This code is wrong or has expired. Check the latest email, or send a new code."))
		return
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, InviteToken{Token: token})
}

// ResendCode handles POST /auth/resend-code (P1-086): 202 whatever the email
// (BR-021); only an unconfirmed signup gets mail (BR-026).
func (s *Server) ResendCode(w http.ResponseWriter, r *http.Request) {
	var body ResendCodeRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	if s.limits != nil && !s.limits.checkLogin(w, r, "code", string(body.Email)) {
		return
	}
	if err := s.team.ResendCode(r.Context(), string(body.Email)); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// AuthProviders handles GET /auth/providers: the client ID for the admin's
// Google button, or null when Google is off (BR-020).
func (s *Server) AuthProviders(w http.ResponseWriter, r *http.Request) {
	var out AuthProviders
	if s.googleClientID != "" {
		id := s.googleClientID
		out.GoogleClientId = &id
	}
	writeJSON(w, http.StatusOK, out)
}

// GoogleSignIn handles POST /auth/google (P1-086, BR-020).
func (s *Server) GoogleSignIn(w http.ResponseWriter, r *http.Request) {
	var body GoogleSignInRequest
	if !decodeJSON(w, r, &body) || !s.googleOn(w, r) {
		return
	}
	session, err := s.auth.GoogleSignIn(r.Context(), s.google, s.googleClientID, body.IdToken)
	switch {
	case errors.Is(err, auth.ErrNoAccount):
		writeError(w, r, apperrors.Unauthenticated("No account uses this Google email. Sign up to open a shop."))
	case errors.Is(err, auth.ErrUnauthenticated):
		writeError(w, r, apperrors.Unauthenticated("Google could not confirm this sign-in.").WithCause(err))
	case err != nil:
		writeError(w, r, err)
	default:
		s.writeSession(w, r, session)
	}
}

// RegisterWithGoogle handles POST /auth/register/google (P1-086, BR-020): the
// shop opens and its owner is signed in, 201.
func (s *Server) RegisterWithGoogle(w http.ResponseWriter, r *http.Request) {
	var body GoogleRegisterRequest
	if !decodeJSON(w, r, &body) || !s.googleOn(w, r) {
		return
	}
	tenantID, ownerID, err := s.team.RegisterWithGoogle(r.Context(), s.google, s.googleClientID, body.IdToken, body.ShopName, clientIP(r))
	if errors.Is(err, auth.ErrUnauthenticated) {
		writeError(w, r, apperrors.Unauthenticated("Google could not confirm this sign-up.").WithCause(err))
		return
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	session, err := s.auth.SessionFor(r.Context(), tenantID, ownerID, auth.RoleOwner)
	if err != nil {
		writeError(w, r, err)
		return
	}
	s.writeSessionStatus(w, r, session, http.StatusCreated)
}

// googleOn refuses the Google routes when no client ID is configured, then
// counts the attempt per IP: the token names nobody until it is checked
// (BR-014).
func (s *Server) googleOn(w http.ResponseWriter, r *http.Request) bool {
	if s.googleClientID == "" || s.google == nil {
		writeError(w, r, fieldErr("id_token", "Google sign-in is not set up."))
		return false
	}
	return s.limits == nil || s.limits.checkLoginIP(w, r)
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

// RequestStaffPasswordReset handles POST /auth/password-reset (P1-088): 202
// whatever the email (BR-021); only an active user gets mail (BR-094).
func (s *Server) RequestStaffPasswordReset(w http.ResponseWriter, r *http.Request) {
	var body StaffPasswordResetRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	if s.limits != nil && !s.limits.checkLogin(w, r, "reset", string(body.Email)) {
		return
	}
	if err := s.team.RequestPasswordReset(r.Context(), string(body.Email)); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// ConfirmStaffPasswordReset handles POST /auth/password-reset/confirm
// (P1-088): the token is the credential; the user is signed in with the new
// password, and out everywhere else (BR-094).
func (s *Server) ConfirmStaffPasswordReset(w http.ResponseWriter, r *http.Request) {
	var body StaffPasswordResetConfirm
	if !decodeJSON(w, r, &body) {
		return
	}
	if s.limits != nil && !s.limits.checkLoginIP(w, r) {
		return
	}
	if len(body.Password) < 8 {
		writeError(w, r, fieldErr("password", "password is at least 8 characters"))
		return
	}
	session, err := s.auth.ResetPassword(r.Context(), s.resets, body.Token, body.Password)
	if errors.Is(err, auth.ErrInvalidReset) {
		writeError(w, r, fieldErr("token", "This reset link has expired or was already used. Ask for a new one."))
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
		Bio: u.Bio, LastLoginAt: u.LastLoginAt, CreatedAt: u.CreatedAt}
}
