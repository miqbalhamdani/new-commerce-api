package storefront

import (
	"net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/miqbalhamdani/new-commerce-api/internal/customers"
	"github.com/miqbalhamdani/new-commerce-api/internal/httpkit"
)

// Customer accounts: 04-api-spec.md §11.8 (P1-206).

func (s *Server) RegisterCustomer(w http.ResponseWriter, r *http.Request) {
	// A plain string, not the generated Email: its own decoder would reject a
	// bad address without naming the field.
	var body struct {
		Email    string  `json:"email"`
		Password string  `json:"password"`
		Name     string  `json:"name"`
		Phone    *string `json:"phone"`
	}
	if !httpkit.DecodeJSON(w, r, &body) {
		return
	}
	t, err := s.customers.Register(r.Context(), body.Email, body.Password, body.Name, body.Phone)
	if err != nil {
		httpkit.WriteError(w, r, err)
		return
	}
	httpkit.WriteJSON(w, http.StatusCreated, tokensOut(t))
}

func (s *Server) Login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !httpkit.DecodeJSON(w, r, &body) {
		return
	}
	t, err := s.customers.Login(r.Context(), body.Email, body.Password)
	if err != nil {
		httpkit.WriteError(w, r, err)
		return
	}
	httpkit.WriteJSON(w, http.StatusOK, tokensOut(t))
}

func (s *Server) Refresh(w http.ResponseWriter, r *http.Request) {
	var body RefreshRequest
	if !httpkit.DecodeJSON(w, r, &body) {
		return
	}
	t, err := s.customers.Refresh(r.Context(), body.RefreshToken)
	if err != nil {
		httpkit.WriteError(w, r, err)
		return
	}
	httpkit.WriteJSON(w, http.StatusOK, tokensOut(t))
}

func (s *Server) Logout(w http.ResponseWriter, r *http.Request) {
	requireCustomer(func(w http.ResponseWriter, r *http.Request, customerID openapi_types.UUID) {
		var body RefreshRequest
		if !httpkit.DecodeJSON(w, r, &body) {
			return
		}
		if err := s.customers.Logout(r.Context(), customerID, body.RefreshToken); err != nil {
			httpkit.WriteError(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})(w, r)
}

func tokensOut(t customers.Tokens) Tokens {
	return Tokens{AccessToken: t.AccessToken, ExpiresIn: t.ExpiresIn, RefreshToken: t.RefreshToken,
		Customer: profileOut(t.Customer)}
}

func profileOut(c customers.Customer) CustomerProfile {
	return CustomerProfile{Id: c.ID, Email: openapi_types.Email(c.Email), Name: c.Name, Phone: c.Phone}
}
