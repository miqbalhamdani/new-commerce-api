package team

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/db/sqlcgen"
)

// AuditFilter is GET /v1/audit-log (P1-077).
type AuditFilter struct {
	SubjectType, SubjectID *string
	ActorID                *uuid.UUID
	From, To               *time.Time
	Before                 *AuditCursor
	Limit                  int
}

// AuditCursor is the position after a page: newest first, the internal id as
// tie-break (BR-005 keeps it out of the entries themselves).
type AuditCursor struct {
	At time.Time `json:"t"`
	ID int64     `json:"i"`
}

type AuditRow = sqlcgen.ListAuditRow

func (s *Service) AuditLog(ctx context.Context, f AuditFilter) ([]AuditRow, *AuditCursor, error) {
	p := sqlcgen.ListAuditParams{SubjectType: f.SubjectType, SubjectID: f.SubjectID, ActorID: f.ActorID,
		FromAt: f.From, ToAt: f.To, Lim: int32(f.Limit + 1)}
	if f.Before != nil {
		p.BeforeAt, p.BeforeID = &f.Before.At, &f.Before.ID
	}
	var rows []AuditRow
	err := s.tx(ctx, func(q *sqlcgen.Queries, _ pgx.Tx) (err error) {
		rows, err = q.ListAudit(ctx, p)
		return err
	})
	if err != nil || len(rows) <= f.Limit {
		return rows, nil, err
	}
	rows = rows[:f.Limit]
	last := rows[f.Limit-1]
	return rows, &AuditCursor{At: last.CreatedAt, ID: last.ID}, nil
}
