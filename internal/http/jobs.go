package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	apperrors "github.com/miqbalhamdani/new-commerce-api/internal/platform/errors"
)

// jobPermission is the permission each kind of job is read with (04-api-spec.md §9).
var jobPermission = map[string]string{
	"product_import":    auth.PermProductsWrite,
	"order_export":      auth.PermExportsRead,
	"channel_import":    auth.PermChannelsRead,
	"image_derivatives": auth.PermMediaRead,
}

// GetJob reports a job's progress. The permission depends on the job's kind,
// so the row is read before the check; another tenant's job is a 404 either
// way (RLS).
func (s *Server) GetJob(w http.ResponseWriter, r *http.Request, id Id) {
	role, ok := auth.RoleFromContext(r.Context())
	if !ok {
		writeError(w, r, apperrors.Unauthenticated("A bearer token is required."))
		return
	}
	j, err := s.jobs.Get(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if perm := jobPermission[j.Kind]; !auth.Can(role, perm) {
		writeError(w, r, apperrors.PermissionDenied(perm))
		return
	}
	out := Job{Id: j.ID, Kind: JobKind(j.Kind), State: JobState(j.State), Processed: int(j.Processed),
		Failed: int(j.Failed), CreatedAt: j.CreatedAt, FinishedAt: j.FinishedAt}
	if j.Total != nil {
		t := int(*j.Total)
		out.Total = &t
	}
	// The row carries a running tally while the job works; result is null
	// until it is done (04-api-spec.md §9).
	if j.Result != nil && j.State == "done" {
		result, err := s.signResult(r, j.Result)
		if err != nil {
			writeError(w, r, err)
			return
		}
		out.Result = &result
	}
	if j.Error != nil {
		var p Problem
		if err := json.Unmarshal(j.Error, &p); err == nil {
			out.Error = &p
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// signResult turns the stored result into the response one. The row keeps
// object keys; every GET signs fresh 15-minute URLs for them (BR-063).
func (s *Server) signResult(r *http.Request, raw []byte) (map[string]any, error) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	if key, ok := m["error_report_key"].(string); ok {
		u, err := s.catalog.SignDownload(r.Context(), key)
		if err != nil {
			return nil, err
		}
		delete(m, "error_report_key")
		m["error_report_url"], m["expires_in"] = u, 900
	} else if _, isImport := m["created"]; isImport {
		m["error_report_url"] = nil
	}
	return m, nil
}
