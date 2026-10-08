package httpapi

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/platform/config"
	"github.com/miqbalhamdani/new-commerce-api/internal/team"
)

// Audit log: 04-api-spec.md §4 (P1-077).

func (s *Server) ListAuditLog(w http.ResponseWriter, r *http.Request, params ListAuditLogParams) {
	requirePermission(auth.PermAuditLogRead, func(w http.ResponseWriter, r *http.Request) {
		f := team.AuditFilter{SubjectType: params.SubjectType, SubjectID: params.SubjectId, ActorID: params.ActorId,
			Limit: pageLimit(params.Limit)}
		var err error
		if f.From, err = timeParam("from", params.From); err != nil {
			writeError(w, r, err)
			return
		}
		if f.To, err = timeParam("to", params.To); err != nil {
			writeError(w, r, err)
			return
		}
		if params.Cursor != nil {
			f.Before = &team.AuditCursor{}
			if err := decodeCursor(params.Cursor, f.Before); err != nil {
				writeError(w, r, err)
				return
			}
		}
		rows, next, err := s.team.AuditLog(r.Context(), f)
		if err != nil {
			writeError(w, r, err)
			return
		}
		page := AuditPage{Data: make([]AuditEntry, 0, len(rows))}
		for _, a := range rows {
			e := AuditEntry{Action: a.Action, SubjectType: a.SubjectType, SubjectId: a.SubjectID, CreatedAt: a.CreatedAt}
			if a.ActorID != nil {
				name := ""
				if a.ActorName != nil {
					name = *a.ActorName
				}
				e.Actor = &Ref{Id: *a.ActorID, Name: name}
			}
			if a.Ip != "" {
				e.Ip = &a.Ip
			}
			e.Before, e.After = jsonObject(a.Before), jsonObject(a.After)
			page.Data = append(page.Data, e)
		}
		if next != nil {
			page.NextCursor = encodeCursor(next)
		}
		writeJSON(w, http.StatusOK, page)
	})(w, r)
}

// timeParam reads a filter time: RFC 3339 with an offset, or a date meaning
// midnight WIB (BR-007). A time with no offset is refused.
func timeParam(name string, v *string) (*time.Time, error) {
	if v == nil || *v == "" {
		return nil, nil
	}
	if t, err := time.Parse(time.RFC3339, *v); err == nil {
		return &t, nil
	}
	if t, err := time.ParseInLocation(time.DateOnly, *v, config.WIB); err == nil {
		return &t, nil
	}
	return nil, fieldErr(name, name+" is an RFC 3339 time with an offset, or a date (midnight WIB)")
}

func jsonObject(raw []byte) *map[string]any {
	if raw == nil {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	return &m
}
