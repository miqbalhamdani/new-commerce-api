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
	apperrors "github.com/miqbalhamdani/new-commerce-api/internal/platform/errors"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

// Register opens a shop (P1-084, BR-020): a tenant and its owner, invited
// until the emailed link confirms the address and sets the password (BR-026).
// It never says whether the email was free (BR-021): a taken email is a nil
// error too, and only bad input or a real failure is not.
func (s *Service) Register(ctx context.Context, shopName, name, address string, ip netip.Addr) error {
	shopName, name, address = strings.TrimSpace(shopName), strings.TrimSpace(name), strings.TrimSpace(address)
	if err := validSignup(shopName, name, address, "name", "email"); err != nil {
		return err
	}
	tenantID, ownerID, err := s.openShop(ctx, shopName, address, "", auth.PlanMulai, ip, uuid.Nil, func(q *sqlcgen.Queries, ctx context.Context, id uuid.UUID) error {
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
	tenantID, ownerID, err = s.openShop(ctx, shopName, id.Email, "google", auth.PlanMulai, ip, uuid.Nil, func(q *sqlcgen.Queries, ctx context.Context, owner uuid.UUID) error {
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

// OpenShopForPlatform is BR-151's open: a shop on plan (BR-160) and its
// invited owner, opened by the platform admin in ctx, who is the audit row's
// actor. The caller has checked the plan. The signup email
// goes out only when sendInvite; otherwise the shop is held until a hand-over.
// A taken email is 422 on owner_email: the caller is a platform admin, so
// saying so names nothing (BR-021 protects the public signup, not this).
func (s *Service) OpenShopForPlatform(ctx context.Context, shopName, name, address, plan string, sendInvite bool) (uuid.UUID, error) {
	shopName, name, address = strings.TrimSpace(shopName), strings.TrimSpace(name), strings.TrimSpace(address)
	if err := validSignup(shopName, name, address, "owner_name", "owner_email"); err != nil {
		return uuid.Nil, err
	}
	actor, _ := tenant.ActorFromContext(ctx)
	tenantID, ownerID, err := s.openShop(ctx, shopName, address, "platform", plan, actor.IP, actor.UserID, func(q *sqlcgen.Queries, ctx context.Context, id uuid.UUID) error {
		_, err := q.InviteUser(ctx, sqlcgen.InviteUserParams{ID: id, Email: address, Name: name, Role: auth.RoleOwner})
		return err
	})
	if taken(err) {
		return uuid.Nil, fieldError("owner_email", "This email already has an account in a shop.")
	}
	if err != nil {
		return uuid.Nil, err
	}
	if sendInvite {
		s.sendSignup(ctx, tenantID, ownerID)
	}
	return tenantID, nil
}

// ChangeInvitedOwner is BR-151's hand-over: a new email or name for a
// shop's owner who has not confirmed yet, audited in that shop. It sends
// nothing. A shop that does not exist is db.ErrNotFound.
//
// ponytail: a signup link already sent to the old address stays valid until
// it expires (BR-026, 7 days): the token names the user, not the address.
// Bind it to the address if hand-overs after a sent invite become common.
func (s *Service) ChangeInvitedOwner(ctx context.Context, shopID uuid.UUID, address, name *string) error {
	if address != nil {
		*address = strings.TrimSpace(*address)
		if !validAddress(*address) {
			return fieldError("email", "email is not a valid address")
		}
	}
	if name != nil {
		*name = strings.TrimSpace(*name)
		if *name == "" || utf8.RuneCountInString(*name) > 100 {
			return fieldError("name", "name is 1 to 100 characters")
		}
	}
	if _, err := s.store.Tenant(ctx, shopID); err != nil {
		return err
	}
	tctx := tenant.NewContext(ctx, shopID)
	err := s.store.InTenantTx(tctx, func(tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		before, err := invitedOwner(tctx, q)
		if err != nil {
			return err
		}
		after, err := q.SetInvitedOwner(tctx, sqlcgen.SetInvitedOwnerParams{ID: before.ID, Email: address, Name: name})
		if errors.Is(err, pgx.ErrNoRows) {
			return ownerConfirmed()
		}
		if err != nil {
			return err
		}
		return db.Audit(tctx, tx, db.AuditEntry{Action: "user.owner_change", SubjectType: "user", SubjectID: before.ID.String(),
			Before: map[string]any{"email": before.Email, "name": before.Name},
			After:  map[string]any{"email": after.Email, "name": after.Name}})
	})
	if taken(err) {
		return fieldError("email", "This email already has an account in a shop.")
	}
	return err
}

// SendOwnerInvite queues the signup email to a shop's unconfirmed owner, at
// their current address (BR-151, BR-026), audited in that shop. A shop that
// does not exist is db.ErrNotFound.
func (s *Service) SendOwnerInvite(ctx context.Context, shopID uuid.UUID) error {
	if _, err := s.store.Tenant(ctx, shopID); err != nil {
		return err
	}
	tctx := tenant.NewContext(ctx, shopID)
	var ownerID uuid.UUID
	err := s.store.InTenantTx(tctx, func(tx pgx.Tx) error {
		o, err := invitedOwner(tctx, sqlcgen.New(tx))
		if err != nil {
			return err
		}
		ownerID = o.ID
		return db.Audit(tctx, tx, db.AuditEntry{Action: "user.resend_signup", SubjectType: "user", SubjectID: o.ID.String()})
	})
	if err == nil {
		s.sendSignup(ctx, shopID, ownerID)
	}
	return err
}

// invitedOwner is the shop's first owner, or 422 when they have confirmed (or
// the shop has none).
func invitedOwner(ctx context.Context, q *sqlcgen.Queries) (sqlcgen.PlatformShopOwnerRow, error) {
	o, err := q.PlatformShopOwner(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return o, apperrors.ValidationFailed("This shop has no owner.")
	}
	if err != nil {
		return o, err
	}
	if o.Status != "invited" {
		return o, ownerConfirmed()
	}
	return o, nil
}

func ownerConfirmed() error {
	return apperrors.ValidationFailed("The owner has confirmed their account; the shop is theirs (BR-151).")
}

// openShop is one signup's transaction: the tenant on plan (BR-160), its
// owner (added by owner) and the tenant.register audit row. The actor is by, or, when by is
// uuid.Nil, the new owner, as the actor of their own signup, as an invitee is
// of their acceptance.
func (s *Service) openShop(ctx context.Context, shopName, address, via, plan string, ip netip.Addr, by uuid.UUID,
	owner func(q *sqlcgen.Queries, ctx context.Context, id uuid.UUID) error) (tenantID, ownerID uuid.UUID, err error) {
	tenantID, ownerID = uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	if by == uuid.Nil {
		by = ownerID
	}
	tctx := tenant.NewActorContext(tenant.NewContext(ctx, tenantID), tenant.Actor{UserID: by, IP: ip})
	err = s.store.InTenantTx(tctx, func(tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		shop, err := q.CreateTenant(tctx, sqlcgen.CreateTenantParams{Name: shopName, Slug: slugFor(shopName), OrderPrefix: prefixFor(shopName), Plan: plan})
		if err != nil {
			return err
		}
		if err := owner(q, tctx, ownerID); err != nil {
			return err
		}
		after := map[string]any{"name": shop.Name, "slug": shop.Slug, "order_prefix": shop.OrderPrefix, "owner": address, "plan": shop.Plan}
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

// validSignup checks a new shop's fields; nameField and emailField are what
// the caller's body calls the owner's name and email.
func validSignup(shopName, name, address, nameField, emailField string) error {
	if shopName == "" || utf8.RuneCountInString(shopName) > 100 {
		return fieldError("shop_name", "shop_name is 1 to 100 characters")
	}
	if name == "" || utf8.RuneCountInString(name) > 100 {
		return fieldError(nameField, nameField+" is 1 to 100 characters")
	}
	if !validAddress(address) {
		return fieldError(emailField, emailField+" is not a valid address")
	}
	return nil
}

// validAddress is a bare address only: "Budi <budi@erigo.co.id>" parses, but
// is not one.
func validAddress(address string) bool {
	a, err := mail.ParseAddress(address)
	return err == nil && a.Address == address && len(address) <= 254
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
