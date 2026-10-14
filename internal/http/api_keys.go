package httpapi

import (
	"net/http"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/team"
)

// API keys: 04-api-spec.md §4 (P1-200).

func (s *Server) ListApiKeys(w http.ResponseWriter, r *http.Request) {
	requirePermission(auth.PermAPIKeysRead, func(w http.ResponseWriter, r *http.Request) {
		rows, err := s.team.APIKeys(r.Context())
		if err != nil {
			writeError(w, r, err)
			return
		}
		list := ApiKeyList{Data: make([]ApiKey, 0, len(rows))}
		for _, k := range rows {
			list.Data = append(list.Data, apiKeyOut(k))
		}
		writeJSON(w, http.StatusOK, list)
	})(w, r)
}

func (s *Server) CreateApiKey(w http.ResponseWriter, r *http.Request) {
	requirePermission(auth.PermAPIKeysWrite, func(w http.ResponseWriter, r *http.Request) {
		var body ApiKeyCreate
		if !decodeJSON(w, r, &body, "key", "key_hash", "last_used_at", "revoked_at", "created_by") {
			return
		}
		k, key, err := s.team.CreateAPIKey(r.Context(), body.Name, body.AllowedOrigin)
		if err != nil {
			writeError(w, r, err)
			return
		}
		out := apiKeyOut(k)
		writeJSON(w, http.StatusCreated, ApiKeyCreated{Id: out.Id, Name: out.Name, Key: key, KeyLast4: out.KeyLast4,
			AllowedOrigin: out.AllowedOrigin, CreatedBy: out.CreatedBy, LastUsedAt: out.LastUsedAt,
			RevokedAt: out.RevokedAt, CreatedAt: out.CreatedAt})
	})(w, r)
}

func (s *Server) UpdateApiKey(w http.ResponseWriter, r *http.Request, id Id) {
	requirePermission(auth.PermAPIKeysWrite, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name          optional[string] `json:"name"`
			AllowedOrigin optional[string] `json:"allowed_origin"`
		}
		if !decodeJSON(w, r, &body, "key", "key_hash", "last_used_at", "revoked_at", "created_by") {
			return
		}
		if err := rejectNull(map[string]bool{"name": body.Name.Null, "allowed_origin": body.AllowedOrigin.Null}); err != nil {
			writeError(w, r, err)
			return
		}
		var name, origin *string
		if body.Name.Set {
			name = &body.Name.Value
		}
		if body.AllowedOrigin.Set {
			origin = &body.AllowedOrigin.Value
		}
		k, err := s.team.UpdateAPIKey(r.Context(), id, name, origin)
		if err != nil {
			writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, apiKeyOut(k))
	})(w, r)
}

func (s *Server) RevokeApiKey(w http.ResponseWriter, r *http.Request, id Id) {
	requirePermission(auth.PermAPIKeysWrite, func(w http.ResponseWriter, r *http.Request) {
		if err := s.team.RevokeAPIKey(r.Context(), id); err != nil {
			writeError(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})(w, r)
}

func apiKeyOut(k team.APIKey) ApiKey {
	out := ApiKey{Id: k.ID, Name: k.Name, KeyLast4: k.KeyLast4, AllowedOrigin: k.AllowedOrigin, LastUsedAt: k.LastUsedAt,
		RevokedAt: k.RevokedAt, CreatedAt: k.CreatedAt}
	if k.CreatedBy != nil && k.CreatedByName != nil {
		out.CreatedBy = &Ref{Id: *k.CreatedBy, Name: *k.CreatedByName}
	}
	return out
}
