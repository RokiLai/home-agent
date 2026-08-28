package api

import (
	"encoding/json"
	"net/http"
	"time"

	"homeagent/internal/auth"
)

type createEnrollmentTokenReq struct {
	TTLSeconds  int    `json:"ttl_seconds"`
	MaxUses     int    `json:"max_uses"`
	Description string `json:"description"`
}

func (s *Server) createEnrollmentToken(w http.ResponseWriter, r *http.Request) {
	if s.EnrollmentManager == nil {
		http.Error(w, `{"error":"server_error","message":"Enrollment manager not initialized"}`, http.StatusInternalServerError)
		return
	}

	defer r.Body.Close()
	var req createEnrollmentTokenReq
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(&req); err != nil {
		http.Error(w, `{"error":"bad_request","message":"Invalid JSON request"}`, http.StatusBadRequest)
		return
	}

	ttl := time.Duration(req.TTLSeconds) * time.Second
	rawToken, tokenInfo, err := s.EnrollmentManager.CreateClaimToken(ttl, req.MaxUses, req.Description)
	if err != nil {
		if s.Log != nil {
			s.Log.Error("create_claim_token_failed", "error", err)
		}
		http.Error(w, `{"error":"server_error","message":"Failed to generate claim token"}`, http.StatusInternalServerError)
		return
	}

	if s.Log != nil {
		s.Log.Info("claim_token_created", "id", tokenInfo.ID, "ttl", ttl.String(), "max_uses", tokenInfo.MaxUses)
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"token":          rawToken,
		"expires_at":     tokenInfo.ExpiresAt.Format(time.RFC3339),
		"max_uses":       tokenInfo.MaxUses,
		"remaining_uses": tokenInfo.RemainingUses,
		"description":    tokenInfo.Description,
	})
}

func (s *Server) listEnrollmentTokens(w http.ResponseWriter, _ *http.Request) {
	if s.EnrollmentManager == nil {
		http.Error(w, `{"error":"server_error","message":"Enrollment manager not initialized"}`, http.StatusInternalServerError)
		return
	}

	tokens := s.EnrollmentManager.ListActiveTokens()
	if tokens == nil {
		tokens = []*auth.ClaimToken{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"tokens": tokens,
	})
}

func (s *Server) deleteEnrollmentToken(w http.ResponseWriter, r *http.Request) {
	if s.EnrollmentManager == nil {
		http.Error(w, `{"error":"server_error","message":"Enrollment manager not initialized"}`, http.StatusInternalServerError)
		return
	}

	tokenID := r.PathValue("id")
	if tokenID == "" {
		http.Error(w, `{"error":"bad_request","message":"Token ID required"}`, http.StatusBadRequest)
		return
	}

	_ = s.EnrollmentManager.RevokeToken(tokenID)
	if s.Log != nil {
		s.Log.Info("claim_token_revoked", "id", tokenID)
	}

	w.WriteHeader(http.StatusNoContent)
}
