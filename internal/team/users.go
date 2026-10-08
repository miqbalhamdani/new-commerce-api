package team

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/db/sqlcgen"
	"github.com/miqbalhamdani/new-commerce-api/internal/email"
	apperrors "github.com/miqbalhamdani/new-commerce-api/internal/platform/errors"
	"github.com/miqbalhamdani/new-commerce-api/internal/queue"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

type User = sqlcgen.User

// Service is users and the tenant's settings.
type Service struct {
	store *db.Store
	queue *queue.Client
}

func NewService(store *db.Store, q *queue.Client) *Service { return &Service{store: store, queue: q} }

// UserCursor is the keyset position after a page of users.
type UserCursor struct {
	At time.Time `json:"t"`
	ID uuid.UUID `json:"i"`
}

func (s *Service) tx(ctx context.Context, fn func(q *sqlcgen.Queries, tx pgx.Tx) error) error {
	return s.store.InTenantTx(ctx, func(tx pgx.Tx) error { return fn(sqlcgen.New(tx), tx) })
}

func (s *Service) ListUsers(ctx context.Context, status, role *string, after *UserCursor, limit int) ([]User, *UserCursor, error) {
	p := sqlcgen.ListUsersParams{Status: status, Role: role, Lim: int32(limit + 1)}
	if after != nil {
		p.AfterAt, p.AfterID = &after.At, &after.ID
	}
	var rows []User
	err := s.tx(ctx, func(q *sqlcgen.Queries, _ pgx.Tx) (err error) {
		rows, err = q.ListUsers(ctx, p)
		return err
	})
	if err != nil || len(rows) <= limit {
		return rows, nil, err
	}
	rows = rows[:limit]
	last := rows[limit-1]
	return rows, &UserCursor{At: last.CreatedAt, ID: last.ID}, nil
}

// Invite creates an invited user and, once that has committed, queues the
// invitation email (BR-026, BR-128). A rolled-back invite sends nothing.
func (s *Service) Invite(ctx context.Context, address, name, role string) (User, error) {
	address, name = strings.TrimSpace(address), strings.TrimSpace(name)
	if name == "" {
		return User{}, fieldError("name", "name is required")
	}
	if err := grantable(ctx, role); err != nil {
		return User{}, err
	}
	var u User
	err := s.tx(ctx, func(q *sqlcgen.Queries, tx pgx.Tx) (err error) {
		u, err = q.InviteUser(ctx, sqlcgen.InviteUserParams{ID: uuid.Must(uuid.NewV7()), Email: address, Name: name, Role: role})
		if code, constraint, ok := db.Violation(err); ok && code == "23505" && constraint == "users_email_key" {
			// Email is unique system-wide (BR-020): a staff email identifies
			// one shop, which is how login finds the tenant.
			return fieldError("email", "this email already has an account")
		}
		if err != nil {
			return err
		}
		return db.Audit(ctx, tx, db.AuditEntry{Action: "user.invite", SubjectType: "user", SubjectID: u.ID.String(),
			After: map[string]any{"email": u.Email, "name": u.Name, "role": u.Role}})
	})
	if err != nil {
		return User{}, err
	}
	s.sendInvitation(ctx, u)
	return u, nil
}

// ResendInvite emails the invitation again while the user is still invited.
func (s *Service) ResendInvite(ctx context.Context, id uuid.UUID) error {
	var u User
	err := s.tx(ctx, func(q *sqlcgen.Queries, tx pgx.Tx) (err error) {
		if u, err = q.GetUser(ctx, id); err != nil {
			return notFound(err)
		}
		if u.Status != "invited" {
			return fieldError("status", "only an invited user can be sent the invitation again")
		}
		return db.Audit(ctx, tx, db.AuditEntry{Action: "user.resend_invite", SubjectType: "user", SubjectID: id.String()})
	})
	if err == nil {
		s.sendInvitation(ctx, u)
	}
	return err
}

// Update changes a role or a status (BR-023, BR-027).
func (s *Service) Update(ctx context.Context, id uuid.UUID, role, status *string) (User, error) {
	if role != nil {
		if err := grantable(ctx, *role); err != nil {
			return User{}, err
		}
	}
	if status != nil && *status != "active" && *status != "disabled" {
		return User{}, fieldError("status", "status is active or disabled")
	}
	var u User
	err := s.tx(ctx, func(q *sqlcgen.Queries, tx pgx.Tx) error {
		before, err := q.GetUser(ctx, id)
		if err != nil {
			return notFound(err)
		}
		if before.Role == auth.RoleOwner && !callerIs(ctx, auth.RoleOwner) {
			return apperrors.Forbidden("Only an owner can change an owner.")
		}
		losesOwner := before.Role == auth.RoleOwner && before.Status == "active" &&
			((role != nil && *role != auth.RoleOwner) || (status != nil && *status == "disabled"))
		if losesOwner {
			others, err := q.OtherActiveOwners(ctx, id)
			if err != nil {
				return err
			}
			if others == 0 {
				return fieldError("role", "the shop's last active owner cannot be demoted or disabled")
			}
		}
		u = before
		if role != nil && *role != before.Role {
			if u, err = q.SetUserRole(ctx, sqlcgen.SetUserRoleParams{ID: id, Role: *role}); err != nil {
				return err
			}
		}
		if status != nil && *status != before.Status {
			if before.Status == "invited" {
				return fieldError("status", "an invited user becomes active by accepting the invitation")
			}
			if u, err = q.SetUserStatus(ctx, sqlcgen.SetUserStatusParams{ID: id, Status: *status}); err != nil {
				return err
			}
			if *status == "disabled" { // out within 15 minutes, when the access token expires (BR-027)
				if _, err := q.RevokeAllUserTokens(ctx, id); err != nil {
					return err
				}
			}
		}
		return db.Audit(ctx, tx, db.AuditEntry{Action: "user.update", SubjectType: "user", SubjectID: id.String(),
			Before: map[string]any{"role": before.Role, "status": before.Status},
			After:  map[string]any{"role": u.Role, "status": u.Status}})
	})
	return u, err
}

func (s *Service) sendInvitation(ctx context.Context, u User) {
	actor, _ := tenant.ActorFromContext(ctx)
	if err := email.Enqueue(ctx, s.queue, email.Envelope{Kind: "invitation", TenantID: u.TenantID,
		UserID: u.ID, ActorID: actor.UserID}); err != nil {
		// The user exists and can be sent the invitation again; a lost email
		// is accepted (BR-128).
		slog.ErrorContext(ctx, "enqueue invitation", "error", err)
	}
}

// grantable refuses a role that is not one of the four, and owner to anyone
// but an owner (BR-023).
func grantable(ctx context.Context, role string) error {
	switch role {
	case auth.RoleOwner:
		if !callerIs(ctx, auth.RoleOwner) {
			return apperrors.Forbidden("Only an owner can grant the owner role.")
		}
	case auth.RoleAdmin, auth.RoleOps, auth.RoleViewer:
	default:
		return fieldError("role", "role is owner, admin, ops or viewer")
	}
	return nil
}

func callerIs(ctx context.Context, role string) bool {
	r, _ := auth.RoleFromContext(ctx)
	return r == role
}

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return apperrors.NotFound("No such user.")
	}
	return err
}

func fieldError(field, detail string) error {
	return apperrors.ValidationFailed(detail).WithFields(apperrors.Field{Name: field, Detail: detail})
}
