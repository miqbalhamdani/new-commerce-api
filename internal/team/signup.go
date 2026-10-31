package team

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/mail"
	"net/netip"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/db/sqlcgen"
	"github.com/miqbalhamdani/new-commerce-api/internal/email"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

// Register opens a shop (P1-084, BR-020): a tenant and its owner, invited
// until the emailed link confirms the address and sets the password (BR-026).
// It never says whether the email was free (BR-021): a taken email is a nil
// error too, and only bad input or a real failure is not.
func (s *Service) Register(ctx context.Context, shopName, name, address string, ip netip.Addr) error {
	shopName, name, address = strings.TrimSpace(shopName), strings.TrimSpace(name), strings.TrimSpace(address)
	if err := validSignup(shopName, name, address); err != nil {
		return err
	}
	tenantID, ownerID, err := s.openShop(ctx, shopName, address, "", ip, func(q *sqlcgen.Queries, ctx context.Context, id uuid.UUID) error {
		_, err := q.InviteUser(ctx, sqlcgen.InviteUserParams{ID: id, Email: address, Name: name, Role: auth.RoleOwner})
		return err
	})
	if taken(err) {
		return s.resendSignup(ctx, address)
	}
	if err != nil {
		return err
	}
	s.sendSignup(ctx, tenantID, ownerID)
	return nil
}

// RegisterWithGoogle opens a shop for the person a Google ID token names
// (BR-020). Google already confirmed the email, so the owner is active, with
// no password and no code to enter. An email that already has an account is
// 422 on email: the caller has proved it is theirs, so saying so names nothing
// they do not control. A token Google does not vouch for is
// auth.ErrUnauthenticated.
func (s *Service) RegisterWithGoogle(ctx context.Context, google *auth.GoogleVerifier, clientID, idToken, shopName string, ip netip.Addr) (tenantID, ownerID uuid.UUID, err error) {
	shopName = strings.TrimSpace(shopName)
	if shopName == "" || utf8.RuneCountInString(shopName) > 100 {
		return uuid.Nil, uuid.Nil, fieldError("shop_name", "shop_name is 1 to 100 characters")
	}
	id, err := google.Verify(ctx, idToken, clientID)
	if err != nil {
		return uuid.Nil, uuid.Nil, auth.ErrUnauthenticated
	}
	name := googleName(id)
	tenantID, ownerID, err = s.openShop(ctx, shopName, id.Email, "google", ip, func(q *sqlcgen.Queries, ctx context.Context, owner uuid.UUID) error {
		_, err := q.CreateActiveOwner(ctx, sqlcgen.CreateActiveOwnerParams{ID: owner, Email: id.Email, Name: name})
		return err
	})
	if taken(err) {
		return uuid.Nil, uuid.Nil, fieldError("email", "This Google email already has an account. Sign in instead.")
	}
	return tenantID, ownerID, err
}

// ResendCode sends a signup's code and link again. Like registering again, it
// answers nothing about the email (BR-021, BR-026).
func (s *Service) ResendCode(ctx context.Context, address string) error {
	return s.resendSignup(ctx, strings.TrimSpace(address))
}

// openShop is one signup's transaction: the tenant, its owner (added by
// owner) and the tenant.register audit row, with the new owner as the actor of
// their own signup, as an invitee is of their acceptance.
func (s *Service) openShop(ctx context.Context, shopName, address, via string, ip netip.Addr,
	owner func(q *sqlcgen.Queries, ctx context.Context, id uuid.UUID) error) (tenantID, ownerID uuid.UUID, err error) {
	tenantID, ownerID = uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	tctx := tenant.NewActorContext(tenant.NewContext(ctx, tenantID), tenant.Actor{UserID: ownerID, IP: ip})
	err = s.store.InTenantTx(tctx, func(tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		shop, err := q.CreateTenant(tctx, sqlcgen.CreateTenantParams{Name: shopName, Slug: slugFor(shopName), OrderPrefix: prefixFor(shopName)})
		if err != nil {
			return err
		}
		if err := owner(q, tctx, ownerID); err != nil {
			return err
		}
		after := map[string]any{"name": shop.Name, "slug": shop.Slug, "order_prefix": shop.OrderPrefix, "owner": address}
		if via != "" {
			after["via"] = via
		}
		return db.Audit(tctx, tx, db.AuditEntry{Action: "tenant.register", SubjectType: "tenant", SubjectID: shop.ID.String(), After: after})
	})
	return tenantID, ownerID, err
}

// taken reports a signup whose email already has an account (BR-020).
func taken(err error) bool {
	code, constraint, ok := db.Violation(err)
	return ok && code == "23505" && constraint == "users_email_key"
}

// googleName is the owner's name from their Google profile, or the part of
// the email before the @ when the profile has none.
func googleName(id auth.GoogleIdentity) string {
	name := strings.TrimSpace(id.Name)
	if name == "" {
		name, _, _ = strings.Cut(id.Email, "@")
	}
	if r := []rune(name); len(r) > 100 {
		name = string(r[:100])
	}
	return name
}

// resendSignup sends the link again to an owner whose signup was never
// confirmed: nobody else in that shop can, so registering again is how a lost
// email is recovered (BR-026). Anyone else gets nothing. The read is the
// staff-login lookup, the one way to find a user before a tenant is known
// (BR-003).
func (s *Service) resendSignup(ctx context.Context, address string) error {
	u, err := s.store.LookupUserForAuth(ctx, address)
	if errors.Is(err, db.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if u.Status == "invited" && u.Role == auth.RoleOwner && u.PasswordHash == nil {
		s.sendSignup(ctx, u.TenantID, u.ID)
	}
	return nil
}

func (s *Service) sendSignup(ctx context.Context, tenantID, ownerID uuid.UUID) {
	if err := email.Enqueue(ctx, s.queue, email.Envelope{Kind: "signup", TenantID: tenantID, UserID: ownerID}); err != nil {
		// Registering again sends it again; a lost email is accepted (BR-128).
		slog.ErrorContext(ctx, "enqueue signup", "error", err)
	}
}

func validSignup(shopName, name, address string) error {
	if shopName == "" || utf8.RuneCountInString(shopName) > 100 {
		return fieldError("shop_name", "shop_name is 1 to 100 characters")
	}
	if name == "" || utf8.RuneCountInString(name) > 100 {
		return fieldError("name", "name is 1 to 100 characters")
	}
	// A bare address only: "Budi <budi@erigo.co.id>" parses, but is not one.
	if a, err := mail.ParseAddress(address); err != nil || a.Address != address || len(address) > 254 {
		return fieldError("email", "email is not a valid address")
	}
	return nil
}

// slugFor derives the tenant's slug, its website's address, from its name.
// The random suffix means two shops with one name never collide; the owner
// picks a cleaner address in settings (BR-140).
func slugFor(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "-"):
			b.WriteByte('-')
		}
		if b.Len() >= 40 {
			break
		}
	}
	base := strings.TrimSuffix(b.String(), "-")
	if base == "" {
		base = "shop"
	}
	suffix := make([]byte, 3)
	_, _ = rand.Read(suffix) // never fails (crypto/rand, Go 1.24+)
	return base + "-" + hex.EncodeToString(suffix)
}

// prefixFor is a first order prefix (BR-029: [A-Z0-9]{2,6}) from the shop's
// name; the owner changes it in onboarding.
func prefixFor(name string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(name) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			if b.Len() == 3 {
				break
			}
		}
	}
	if b.Len() < 2 {
		return "SHOP"
	}
	return b.String()
}
