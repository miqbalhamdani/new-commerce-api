// Package platformadmin is BR-150's report and BR-151's shop actions, for the
// staff in PLATFORM_ADMIN_EMAILS. It is the one request path that spans shops
// (BR-003's exception), and it spans them the way the retention sweep does:
// shop ids from tenants, which has no RLS, then each shop read or written in
// its own InTenantTx as app_user, audited there.
package platformadmin

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/db/sqlcgen"
	"github.com/miqbalhamdani/new-commerce-api/internal/platform/config"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

// The report's windows (BR-150).
const (
	recent    = 7 * 24 * time.Hour
	month     = 30 * 24 * time.Hour
	sleepsFor = 14 * 24 * time.Hour
)

// Sources: the signup form, Google, cmd/create-tenant and cmd/demo-seed
// (which write no tenant.register audit row), or a platform admin (BR-151).
const (
	SourceEmail    = "email"
	SourceGoogle   = "google"
	SourceCLI      = "cli"
	SourcePlatform = "platform"
)

type Service struct {
	store  *db.Store
	admins auth.PlatformAdmins
	now    func() time.Time
}

func NewService(store *db.Store, admins auth.PlatformAdmins) *Service {
	return &Service{store: store, admins: admins, now: time.Now}
}

// Allows reports whether the signed-in user may open the report: active, and
// their email in PLATFORM_ADMIN_EMAILS. Their row is read in their own shop,
// the tenant their token names. A nil Service, or an empty list, allows no one.
func (s *Service) Allows(ctx context.Context) (bool, error) {
	if s == nil || len(s.admins) == 0 {
		return false, nil
	}
	actor, ok := tenant.ActorFromContext(ctx)
	if !ok {
		return false, nil
	}
	var allowed bool
	err := s.store.InTenantTx(ctx, func(tx pgx.Tx) error {
		row, err := sqlcgen.New(tx).PlatformActor(ctx, actor.UserID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		allowed = row.Status == "active" && s.admins.Has(row.Email)
		return nil
	})
	return allowed, err
}

type Report struct {
	GeneratedAt time.Time
	Summary     Summary
	Shops       []Shop
}

// Summary counts only the shops that signed up themselves.
type Summary struct {
	Shops, New7d, New30d, Verified, WebsiteSetUp, WithProducts, WithOrders, Active7d, Sleeping int
}

type Shop struct {
	ID             uuid.UUID
	Name, Slug     string
	Status         string
	Plan           string
	PlanUntil      *time.Time // a date (BR-160)
	BusinessType   string     // BR-170
	CreatedAt      time.Time
	Source         string
	Owner          *Owner
	Website        Website
	Stats          db.ShopStats
	LastActivityAt *time.Time
}

type Owner struct {
	Name, Email, Status string
	LastLoginAt         *time.Time
}

type Website struct {
	Theme                 *string
	Logo, WhatsApp, SetUp bool
}

// Report reads every shop, newest first.
//
// ponytail: one transaction per shop per request, fine for hundreds of shops;
// past that, have the worker write a nightly snapshot and serve that instead.
func (s *Service) Report(ctx context.Context) (Report, error) {
	tenants, err := s.store.Tenants(ctx)
	if err != nil {
		return Report{}, err
	}
	now := s.now().In(config.WIB)
	out := Report{GeneratedAt: now, Shops: make([]Shop, 0, len(tenants))}
	for _, t := range tenants {
		shop, err := s.shop(ctx, t)
		if err != nil {
			return Report{}, fmt.Errorf("shop %s: %w", t.ID, err)
		}
		out.Shops = append(out.Shops, shop)
		count(&out.Summary, shop, now)
	}
	return out, nil
}

// SetStatus is BR-151's status: active, suspended or closed. Anything but
// active also revokes every refresh token in the shop; sign-in and refresh
// then refuse it (auth.Service.issue) and the storefront API answers 404.
// A shop that does not exist is db.ErrNotFound.
func (s *Service) SetStatus(ctx context.Context, id uuid.UUID, status string) error {
	if _, err := s.store.Tenant(ctx, id); err != nil {
		return err
	}
	tctx := tenant.NewContext(ctx, id)
	return s.store.InTenantTx(tctx, func(tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		before, err := q.SetShopStatus(tctx, status)
		if err != nil {
			return err
		}
		if status != "active" {
			if _, err := q.RevokeShopRefreshTokens(tctx); err != nil {
				return err
			}
		}
		return db.Audit(tctx, tx, db.AuditEntry{Action: "tenant.status", SubjectType: "tenant", SubjectID: id.String(),
			Before: map[string]any{"status": before}, After: map[string]any{"status": status}})
	})
}

// SetPlan is BR-160's plan and the date its paid period is expected to end
// (nil for none), audited tenant.plan in the shop. The caller has checked the
// plan. A shop that does not exist is db.ErrNotFound.
func (s *Service) SetPlan(ctx context.Context, id uuid.UUID, plan string, until *time.Time) error {
	if _, err := s.store.Tenant(ctx, id); err != nil {
		return err
	}
	var date pgtype.Date
	if until != nil {
		date = pgtype.Date{Time: *until, Valid: true}
	}
	tctx := tenant.NewContext(ctx, id)
	return s.store.InTenantTx(tctx, func(tx pgx.Tx) error {
		before, err := sqlcgen.New(tx).SetShopPlan(tctx, sqlcgen.SetShopPlanParams{Plan: plan, PlanUntil: date})
		if err != nil {
			return err
		}
		return db.Audit(tctx, tx, db.AuditEntry{Action: "tenant.plan", SubjectType: "tenant", SubjectID: id.String(),
			Before: map[string]any{"plan": before.Plan, "plan_until": dateOf(before.PlanUntil)},
			After:  map[string]any{"plan": plan, "plan_until": dateOf(date)}})
	})
}

// SetBusinessType is BR-170's type, audited tenant.business_type in the
// shop. The caller has checked it. A shop that does not exist is
// db.ErrNotFound.
func (s *Service) SetBusinessType(ctx context.Context, id uuid.UUID, businessType string) error {
	if _, err := s.store.Tenant(ctx, id); err != nil {
		return err
	}
	tctx := tenant.NewContext(ctx, id)
	return s.store.InTenantTx(tctx, func(tx pgx.Tx) error {
		before, err := sqlcgen.New(tx).SetShopBusinessType(tctx, businessType)
		if err != nil {
			return err
		}
		return db.Audit(tctx, tx, db.AuditEntry{Action: "tenant.business_type", SubjectType: "tenant", SubjectID: id.String(),
			Before: map[string]any{"business_type": before}, After: map[string]any{"business_type": businessType}})
	})
}

// dateOf is a date for the audit row: YYYY-MM-DD, or nil for none.
func dateOf(d pgtype.Date) any {
	if !d.Valid {
		return nil
	}
	return d.Time.Format(time.DateOnly)
}

// Shop is one shop as the report shows it, for the answer to a shop action
// (BR-151). A shop that does not exist is db.ErrNotFound.
func (s *Service) Shop(ctx context.Context, id uuid.UUID) (Shop, error) {
	t, err := s.store.Tenant(ctx, id)
	if err != nil {
		return Shop{}, err
	}
	return s.shop(ctx, t)
}

func (s *Service) shop(ctx context.Context, t db.TenantRow) (Shop, error) {
	shop := Shop{ID: t.ID, Name: t.Name, Slug: t.Slug, Status: t.Status, Plan: t.Plan, PlanUntil: t.PlanUntil,
		BusinessType: t.BusinessType,
		CreatedAt:    t.CreatedAt}
	err := s.store.InTenantTx(tenant.NewContext(ctx, t.ID), func(tx pgx.Tx) error {
		st, err := db.PlatformShopStats(ctx, tx)
		if err != nil {
			return err
		}
		shop.Stats = st
		shop.Source = SourceCLI
		if st.Registered {
			shop.Source = SourceEmail
			if v := st.RegisterVia; v != nil && (*v == SourceGoogle || *v == SourcePlatform) {
				shop.Source = *v
			}
		}
		shop.LastActivityAt = latest(st.LastAuditAt, st.LastLoginAt)

		q := sqlcgen.New(tx)
		owner, err := q.PlatformShopOwner(ctx)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return err
		default:
			shop.Owner = &Owner{Name: owner.Name, Email: owner.Email, Status: owner.Status, LastLoginAt: owner.LastLoginAt}
		}

		web, err := q.PlatformShopWebsite(ctx)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return err
		default:
			shop.Website = Website{Theme: &web.Theme, Logo: web.Logo, WhatsApp: web.Whatsapp,
				SetUp: web.Logo || web.Whatsapp || web.Tagline}
		}
		return nil
	})
	return shop, err
}

// count adds one shop to the summary, if it signed up itself (BR-150).
func count(sum *Summary, shop Shop, now time.Time) {
	if shop.Source != SourceEmail && shop.Source != SourceGoogle {
		return
	}
	sum.Shops++
	if now.Sub(shop.CreatedAt) <= recent {
		sum.New7d++
	}
	if now.Sub(shop.CreatedAt) <= month {
		sum.New30d++
	}
	verified := shop.Owner != nil && shop.Owner.Status == "active"
	if verified {
		sum.Verified++
	}
	if shop.Website.SetUp {
		sum.WebsiteSetUp++
	}
	if shop.Stats.ProductsTotal > 0 {
		sum.WithProducts++
	}
	if shop.Stats.OrdersTotal > 0 {
		sum.WithOrders++
	}
	active := shop.LastActivityAt != nil && now.Sub(*shop.LastActivityAt) <= recent
	if active {
		sum.Active7d++
	}
	if verified && (shop.LastActivityAt == nil || now.Sub(*shop.LastActivityAt) > sleepsFor) {
		sum.Sleeping++
	}
}

// latest is the later of two optional times.
func latest(a, b *time.Time) *time.Time {
	switch {
	case a == nil:
		return b
	case b == nil || a.After(*b):
		return a
	}
	return b
}
