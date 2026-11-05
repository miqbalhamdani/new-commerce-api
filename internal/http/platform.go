package httpapi

import (
	"net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
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
		out.Shops = append(out.Shops, shop)
	}
	return out
}
