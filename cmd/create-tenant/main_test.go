package main

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/platform/config"
)

func TestValidate(t *testing.T) {
	ok := shop{Name: "Belli", Slug: "belli-id", OrderPrefix: "BLI", OwnerEmail: "o@belli.id", OwnerName: "Owner"}
	if err := ok.validate(); err != nil {
		t.Fatalf("valid shop: %v", err)
	}
	for name, mutate := range map[string]func(*shop){
		"lowercase prefix": func(s *shop) { s.OrderPrefix = "bli" },
		"long prefix":      func(s *shop) { s.OrderPrefix = "BELLIID" },
		"slug with space":  func(s *shop) { s.Slug = "belli id" },
		"uppercase slug":   func(s *shop) { s.Slug = "Belli" },
		"no name":          func(s *shop) { s.Name = "" },
		"no email":         func(s *shop) { s.OwnerEmail = "" },
		"no owner name":    func(s *shop) { s.OwnerName = "" },
	} {
		s := ok
		mutate(&s)
		if s.validate() == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

// TestCreate proves the owner can sign in the way the API signs them in, and
// that a second shop with the same slug or owner email leaves nothing behind.
func TestCreate(t *testing.T) {
	ctx := t.Context()
	store, err := db.New(ctx, config.AppDatabaseURL())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(store.Close)

	suffix := uuid.Must(uuid.NewV7()).String()[:8]
	s := shop{
		Name: "Create Tenant Test", Slug: "create-tenant-" + suffix, OrderPrefix: "CTT",
		OwnerEmail: "owner-" + suffix + "@example.com", OwnerName: "Test Owner",
	}
	tenantID, err := create(ctx, store, s, "a-password-for-tests")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	u, err := store.LookupUserForAuth(ctx, s.OwnerEmail)
	if err != nil {
		t.Fatalf("look up owner: %v", err)
	}
	if u.TenantID != tenantID || u.Role != "owner" || u.Status != "active" {
		t.Errorf("owner = tenant %s role %q status %q, want %s owner active", u.TenantID, u.Role, u.Status, tenantID)
	}
	if u.PasswordHash == nil {
		t.Fatal("owner has no password hash")
	}
	if err := auth.VerifyPassword(*u.PasswordHash, "a-password-for-tests"); err != nil {
		t.Errorf("password does not verify: %v", err)
	}

	dupSlug := s
	dupSlug.OwnerEmail = "other-" + suffix + "@example.com"
	if _, err := create(ctx, store, dupSlug, "x"); err == nil || !strings.Contains(err.Error(), "insert shop") {
		t.Errorf("duplicate slug: err = %v, want an insert shop error", err)
	}
	if _, err := store.LookupUserForAuth(ctx, dupSlug.OwnerEmail); err == nil {
		t.Error("duplicate slug left its owner behind")
	}

	dupEmail := s
	dupEmail.Slug = "create-tenant-other-" + suffix
	if _, err := create(ctx, store, dupEmail, "x"); err == nil || !strings.Contains(err.Error(), "insert owner") {
		t.Errorf("duplicate email: err = %v, want an insert owner error", err)
	}
}
