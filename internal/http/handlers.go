package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
)

// refreshCookieName is the only place the refresh token lives on a client.
const refreshCookieName = "refresh_token"

// Server implements the generated ServerInterface.
//
// It is deliberately thin: decode, call internal/auth, encode. Anything that
// looks like a decision belongs in the service, where it can be tested without
// an HTTP request.
type Server struct {
	auth *auth.Service

	// secureCookies is false only for local development over plain HTTP, where
	// a Secure cookie would be dropped by the browser and nothing would work.
	secureCookies bool
}

func NewServer(authSvc *auth.Service, secureCookies bool) *Server {
	return &Server{auth: authSvc, secureCookies: secureCookies}
}

// Login handles POST /auth/login.
func (s *Server) Login(w http.ResponseWriter, r *http.Request) {
	var body LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeProblem(w, r, http.StatusUnprocessableEntity, "validation_failed",
			"Validation failed", "The request body is not valid JSON.")
		return
	}
	if body.Email == "" || len(body.Password) < 8 {
		writeProblem(w, r, http.StatusUnprocessableEntity, "validation_failed",
			"Validation failed", "email and password are required; password is at least 8 characters.")
		return
	}

	session, err := s.auth.Login(r.Context(), string(body.Email), body.Password)
	if err != nil {
		s.writeAuthError(w, r, err)
		return
	}
	s.writeSession(w, r, session)
}

// Refresh handles POST /auth/refresh.
func (s *Server) Refresh(w http.ResponseWriter, r *http.Request) {
	presented, _ := r.Cookie(refreshCookieName)
	if presented == nil {
		s.writeAuthError(w, r, auth.ErrUnauthenticated)
		return
	}

	session, err := s.auth.Refresh(r.Context(), presented.Value)
	if err != nil {
		// Clear the cookie on the way out. If the token was reused, every
		// session is now revoked and the browser holding this one should stop
		// presenting it.
		http.SetCookie(w, s.clearRefreshCookie())
		s.writeAuthError(w, r, err)
		return
	}
	s.writeSession(w, r, session)
}

// Logout handles POST /auth/logout.
func (s *Server) Logout(w http.ResponseWriter, r *http.Request) {
	var presented string
	if c, _ := r.Cookie(refreshCookieName); c != nil {
		presented = c.Value
	}
	if err := s.auth.Logout(r.Context(), presented); err != nil {
		writeProblem(w, r, http.StatusInternalServerError, "internal",
			"Internal error", "Could not complete sign out.")
		return
	}
	http.SetCookie(w, s.clearRefreshCookie())
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) writeSession(w http.ResponseWriter, r *http.Request, session auth.Session) {
	http.SetCookie(w, s.refreshCookie(session.RefreshToken, auth.RefreshTokenTTL))

	body := Session{
		AccessToken: session.AccessToken,
		ExpiresIn:   session.ExpiresIn,
		User: SessionUser{
			Id:          session.User.ID,
			Name:        session.User.Name,
			Role:        SessionUserRole(session.User.Role),
			Permissions: session.User.Permissions,
		},
		Tenant: SessionTenant{
			Id:       session.Tenant.ID,
			Name:     session.Tenant.Name,
			Timezone: session.Tenant.Timezone,
			Currency: session.Tenant.Currency,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(body); err != nil {
		// The status is already written; there is nothing left to tell the
		// client. Logged by the middleware.
		_ = err
	}
}

func (s *Server) writeAuthError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, auth.ErrUnauthenticated) {
		writeProblem(w, r, http.StatusUnauthorized, "unauthenticated",
			"Unauthenticated", "Email or password is incorrect.")
		return
	}
	writeProblem(w, r, http.StatusInternalServerError, "internal",
		"Internal error", "Something went wrong.")
}

// refreshCookie builds the cookie the refresh token travels in.
//
// httpOnly so no script on the page can read it -- that is the whole reason the
// token is not in the response body. SameSite=Lax so it is not sent on a
// cross-site POST. Path is the auth routes only, so it is not attached to every
// request to the API.
func (s *Server) refreshCookie(value string, ttl time.Duration) *http.Cookie {
	return &http.Cookie{
		Name:     refreshCookieName,
		Value:    value,
		Path:     "/v1/auth",
		HttpOnly: true,
		Secure:   s.secureCookies,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(ttl),
	}
}

func (s *Server) clearRefreshCookie() *http.Cookie {
	c := s.refreshCookie("", 0)
	c.MaxAge = -1
	c.Expires = time.Unix(0, 0)
	return c
}
