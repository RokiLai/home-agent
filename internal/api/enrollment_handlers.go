package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"homeagent/internal/auth"
)

type createEnrollmentTokenReq struct {
	TTLSeconds  int    `json:"ttl_seconds"`
	MaxUses     int    `json:"max_uses"`
	Description string `json:"description"`
	OwnerUserID string `json:"owner_user_id,omitempty"`
}

func (s *Server) createEnrollmentToken(w http.ResponseWriter, r *http.Request) {
	if s.EnrollmentManager == nil {
		http.Error(w, `{"error":"server_error","message":"Enrollment manager not initialized"}`, http.StatusInternalServerError)
		return
	}

	actor := auth.GetActorFromContext(r.Context())
	actorID := ""
	actorRole := auth.RoleOwner
	if actor != nil {
		actorID = actor.UserID
		actorRole = actor.Role
	}

	defer r.Body.Close()
	var req createEnrollmentTokenReq
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(&req); err != nil {
		http.Error(w, `{"error":"bad_request","message":"Invalid JSON request"}`, http.StatusBadRequest)
		return
	}

	// 所有权冻结策略：
	// Owner 角色可指定任意启用用户的 OwnerUserID（若未指定默认为自身）；
	// Admin 角色只能以自己作为 OwnerUserID
	ownerUserID := actorID
	if actorRole == auth.RoleOwner && strings.TrimSpace(req.OwnerUserID) != "" {
		ownerUserID = strings.TrimSpace(req.OwnerUserID)
	}

	ttl := time.Duration(req.TTLSeconds) * time.Second
	rawToken, tokenInfo, err := s.EnrollmentManager.CreateClaimTokenForOwner(ttl, req.MaxUses, req.Description, actorID, ownerUserID)
	if err != nil {
		if s.Log != nil {
			s.Log.Error("create_claim_token_failed", "error", err)
		}
		http.Error(w, `{"error":"server_error","message":"Failed to generate claim token"}`, http.StatusInternalServerError)
		return
	}

	if s.Log != nil {
		s.Log.Info("claim_token_created", "id", tokenInfo.ID, "ttl", ttl.String(), "max_uses", tokenInfo.MaxUses, "owner_user_id", ownerUserID, "created_by", actorID)
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"token":          rawToken,
		"expires_at":     tokenInfo.ExpiresAt.Format(time.RFC3339),
		"max_uses":       tokenInfo.MaxUses,
		"remaining_uses": tokenInfo.RemainingUses,
		"description":    tokenInfo.Description,
		"owner_user_id":  tokenInfo.OwnerUserID,
	})
}

func (s *Server) listEnrollmentTokens(w http.ResponseWriter, r *http.Request) {
	if s.EnrollmentManager == nil {
		http.Error(w, `{"error":"server_error","message":"Enrollment manager not initialized"}`, http.StatusInternalServerError)
		return
	}

	actor := auth.GetActorFromContext(r.Context())
	tokens := s.EnrollmentManager.ListActiveTokens()
	if tokens == nil {
		tokens = []*auth.ClaimToken{}
	}

	var visibleTokens []*auth.ClaimToken
	for _, t := range tokens {
		if actor == nil || actor.Role == auth.RoleOwner || t.CreatedByUserID == actor.UserID || t.OwnerUserID == actor.UserID {
			visibleTokens = append(visibleTokens, t)
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"tokens": visibleTokens,
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
