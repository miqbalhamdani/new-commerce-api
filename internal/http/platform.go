package httpapi

import (
	"errors"
	"net/http"

	"github.com/google/uuid"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	apperrors "github.com/miqbalhamdani/new-commerce-api/internal/platform/errors"
	"github.com/miqbalhamdani/new-commerce-api/internal/platformadmin"
)

// GetPlatformShops handles GET /v1/platform/shops (P1-902, 04-api-spec.md
// §14): BR-150's report, for the platform admins only.
func (s *Server) GetPlatformShops(w http.ResponseWriter, r *http.Request) {
	s.requirePlatformAdmin(func(w http.ResponseWriter, r *http.Request) {
		rep, err := s.platform.Report(r.Context())
		if err != nil {
			writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, platformOut(rep))
	})(w, r)
}

// CreatePlatformShop handles POST /v1/platform/shops (P1-912, §14.1): a shop
// held for a hand-over, or invited at once (BR-151).
func (s *Server) CreatePlatformShop(w http.ResponseWriter, r *http.Request) {
	s.requirePlatformAdmin(func(w http.ResponseWriter, r *http.Request) {
		var body PlatformShopCreate
		if !decodeJSON(w, r, &body) {
			return
		}
		id, err := s.team.OpenShopForPlatform(r.Context(), body.ShopName, body.OwnerName, string(body.OwnerEmail), body.SendInvite)
		if err != nil {
			writeError(w, r, err)
			return
		}
		s.writePlatformShop(w, r, http.StatusCreated, id)
	})(w, r)
}

// UpdatePlatformShop handles PATCH /v1/platform/shops/{id} (P1-913, §14.1):
// suspend, close or reopen a shop (BR-151).
func (s *Server) UpdatePlatformShop(w http.ResponseWriter, r *http.Request, id Id) {
	s.requirePlatformAdmin(func(w http.ResponseWriter, r *http.Request) {
		var body PlatformShopUpdate
		if !decodeJSON(w, r, &body) {
			return
		}
		if !body.Status.Valid() {
			writeError(w, r, apperrors.ValidationFailed("status is active, suspended or closed").
				WithFields(apperrors.Field{Name: "status", Detail: "is active, suspended or closed"}))
			return
		}
		if err := s.platform.SetStatus(r.Context(), id, string(body.Status)); err != nil {
			writeError(w, r, notFoundShop(err))
			return
		}
		s.writePlatformShop(w, r, http.StatusOK, id)
	})(w, r)
}

// UpdatePlatformShopOwner handles PATCH /v1/platform/shops/{id}/owner
// (P1-914, §14.1): the hand-over, while the owner is invited (BR-151).
func (s *Server) UpdatePlatformShopOwner(w http.ResponseWriter, r *http.Request, id Id) {
	s.requirePlatformAdmin(func(w http.ResponseWriter, r *http.Request) {
		var body PlatformShopOwnerUpdate
		if !decodeJSON(w, r, &body) {
			return
		}
		var address *string
		if body.Email != nil {
			a := string(*body.Email)
			address = &a
		}
		if err := s.team.ChangeInvitedOwner(r.Context(), id, address, body.Name); err != nil {
			writeError(w, r, notFoundShop(err))
			return
		}
		s.writePlatformShop(w, r, http.StatusOK, id)
	})(w, r)
}

// SendPlatformShopInvite handles POST /v1/platform/shops/{id}/invite
// (P1-914, §14.1): the signup email, to the invited owner (BR-151, BR-026).
func (s *Server) SendPlatformShopInvite(w http.ResponseWriter, r *http.Request, id Id) {
	s.requirePlatformAdmin(func(w http.ResponseWriter, r *http.Request) {
		if err := s.team.SendOwnerInvite(r.Context(), id); err != nil {
			writeError(w, r, notFoundShop(err))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})(w, r)
}

// ImpersonatePlatformShop handles POST /v1/platform/shops/{id}/impersonate
// (P1-915, §14.2): an owner's session in that shop, with no cookie (BR-152).
func (s *Server) ImpersonatePlatformShop(w http.ResponseWriter, r *http.Request, id Id) {
	s.requirePlatformAdmin(func(w http.ResponseWriter, r *http.Request) {
		session, err := s.auth.Impersonate(r.Context(), id)
		if err != nil {
			writeError(w, r, notFoundShop(err))
			return
		}
		s.writeSession(w, r, session)
	})(w, r)
}

// writePlatformShop answers a shop action with the shop as the report shows
// it. A shop that does not exist is 404.
func (s *Server) writePlatformShop(w http.ResponseWriter, r *http.Request, status int, id uuid.UUID) {
	sh, err := s.platform.Shop(r.Context(), id)
	if err != nil {
		writeError(w, r, notFoundShop(err))
		return
	}
	writeJSON(w, status, platformShopOut(sh))
}

// notFoundShop is 404 for a shop id that names no shop.
func notFoundShop(err error) error {
	if errors.Is(err, db.ErrNotFound) {
		return apperrors.NotFound("No such shop.")
	}
	return err
}

// requirePlatformAdmin is requirePermission for BR-150, which is not a role:
// it runs next only for an active user whose email is in
// PLATFORM_ADMIN_EMAILS, checked on every request rather than trusted from the
// session.
func (s *Server) requirePlatformAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.RoleFromContext(r.Context()); !ok {
			writeError(w, r, apperrors.Unauthenticated("A bearer token is required."))
			return
		}
		ok, err := s.platform.Allows(r.Context())
		if err != nil {
			writeError(w, r, err)
			return
		}
		if !ok {
			writeError(w, r, apperrors.Forbidden(
				"Only Belli's platform admins can open this: an active user whose email is in PLATFORM_ADMIN_EMAILS (BR-150)."))
			return
		}
		next(w, r)
	}
}

func platformOut(rep platformadmin.Report) PlatformShops {
	sum := rep.Summary
	out := PlatformShops{
		GeneratedAt: rep.GeneratedAt,
		Summary: PlatformSummary{Shops: sum.Shops, New7d: sum.New7d, New30d: sum.New30d, Verified: sum.Verified,
			WebsiteSetUp: sum.WebsiteSetUp, WithProducts: sum.WithProducts, WithOrders: sum.WithOrders,
			Active7d: sum.Active7d, Sleeping: sum.Sleeping},
		Shops: make([]PlatformShop, 0, len(rep.Shops)),
	}
	for _, sh := range rep.Shops {
		out.Shops = append(out.Shops, platformShopOut(sh))
	}
	return out
}

func platformShopOut(sh platformadmin.Shop) PlatformShop {
	st := sh.Stats
	shop := PlatformShop{
		Id: sh.ID, Name: sh.Name, Slug: sh.Slug, Status: PlatformShopStatus(sh.Status), CreatedAt: sh.CreatedAt,
		Source: PlatformShopSource(sh.Source),
		Website: PlatformShopWebsite{Theme: sh.Website.Theme, Logo: sh.Website.Logo, Whatsapp: sh.Website.WhatsApp,
			SetUp: sh.Website.SetUp},
		Products:       PlatformShopProducts{Active: int(st.ProductsActive), Total: int(st.ProductsTotal), FirstAt: st.FirstProductAt},
		Orders:         PlatformShopOrders{Total: int(st.OrdersTotal), Last30d: int(st.Orders30d), FirstAt: st.FirstOrderAt, LastAt: st.LastOrderAt},
		LastActivityAt: sh.LastActivityAt,
	}
	if o := sh.Owner; o != nil {
		shop.Owner = &PlatformShopOwner{Name: o.Name, Email: openapi_types.Email(o.Email), Status: PlatformShopOwnerStatus(o.Status), LastLoginAt: o.LastLoginAt}
	}
	return shop
}
