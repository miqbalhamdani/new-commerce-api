package httpapi

import (
	"net/http"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/team"
)

// Settings: 04-api-spec.md §4 (P1-071).

func (s *Server) GetSettings(w http.ResponseWriter, r *http.Request) {
	requirePermission(auth.PermSettingsRead, func(w http.ResponseWriter, r *http.Request) {
		t, err := s.team.Settings(r.Context())
		if err != nil {
			writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, settingsOut(t))
	})(w, r)
}

func (s *Server) UpdateSettings(w http.ResponseWriter, r *http.Request) {
	requirePermission(auth.PermSettingsWrite, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name        optional[string] `json:"name"`
			Timezone    optional[string] `json:"timezone"`
			OrderPrefix optional[string] `json:"order_prefix"`
		}
		if !decodeJSON(w, r, &body, "slug", "status") {
			return
		}
		if err := rejectNull(map[string]bool{"name": body.Name.Null, "timezone": body.Timezone.Null,
			"order_prefix": body.OrderPrefix.Null}); err != nil {
			writeError(w, r, err)
			return
		}
		var name, tz, prefix *string
		if body.Name.Set {
			name = &body.Name.Value
		}
		if body.Timezone.Set {
			tz = &body.Timezone.Value
		}
		if body.OrderPrefix.Set {
			prefix = &body.OrderPrefix.Value
		}
		t, err := s.team.UpdateSettings(r.Context(), name, tz, prefix)
		if err != nil {
			writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, settingsOut(t))
	})(w, r)
}

func settingsOut(t team.Settings) Settings {
	return Settings{Id: t.ID, Name: t.Name, Slug: t.Slug, OrderPrefix: t.OrderPrefix, Timezone: t.Timezone,
		Status: SettingsStatus(t.Status)}
}
