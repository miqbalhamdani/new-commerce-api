package team

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/db/sqlcgen"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

type Settings = sqlcgen.Tenant

var orderPrefix = regexp.MustCompile(`^[A-Z0-9]{2,6}$`)

// Settings reads the caller's own shop. tenants has no RLS; the id comes
// from the caller's token and nowhere else (BR-003).
func (s *Service) Settings(ctx context.Context) (Settings, error) {
	id, _ := tenant.FromContext(ctx)
	var t Settings
	err := s.tx(ctx, func(q *sqlcgen.Queries, _ pgx.Tx) (err error) {
		t, err = q.GetTenant(ctx, id)
		return err
	})
	return t, err
}

// UpdateSettings changes the name, display time zone or order prefix (BR-029,
// BR-077). A nil field is left alone.
func (s *Service) UpdateSettings(ctx context.Context, name, timezone, prefix *string) (Settings, error) {
	if name != nil {
		n := strings.TrimSpace(*name)
		if n == "" {
			return Settings{}, fieldError("name", "name cannot be empty")
		}
		name = &n
	}
	if timezone != nil {
		if _, err := time.LoadLocation(*timezone); err != nil || *timezone == "" || *timezone == "Local" {
			return Settings{}, fieldError("timezone", "timezone is an IANA name such as Asia/Jakarta")
		}
	}
	if prefix != nil && !orderPrefix.MatchString(*prefix) {
		return Settings{}, fieldError("order_prefix", "order_prefix is 2 to 6 upper-case letters or digits")
	}
	var after Settings
	err := s.tx(ctx, func(q *sqlcgen.Queries, tx pgx.Tx) error {
		id, _ := tenant.FromContext(ctx)
		before, err := q.GetTenant(ctx, id)
		if err != nil {
			return err
		}
		if after, err = q.UpdateSettings(ctx, sqlcgen.UpdateSettingsParams{Name: name, Timezone: timezone, OrderPrefix: prefix}); err != nil {
			return err
		}
		return db.Audit(ctx, tx, db.AuditEntry{Action: "settings.update", SubjectType: "tenant", SubjectID: id.String(),
			Before: map[string]any{"name": before.Name, "timezone": before.Timezone, "order_prefix": before.OrderPrefix},
			After:  map[string]any{"name": after.Name, "timezone": after.Timezone, "order_prefix": after.OrderPrefix}})
	})
	return after, err
}
