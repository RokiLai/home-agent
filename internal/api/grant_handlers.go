package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"homeagent/internal/auth"
	"homeagent/internal/device"
)

type setGrantReq struct {
	Level string `json:"level"`
}

type transferDeviceReq struct {
	NewOwnerID     string             `json:"new_owner_id"`
	RetainGrant    *device.GrantLevel `json:"retain_grant,omitempty"`
}

func (s *Server) listDeviceGrants(w http.ResponseWriter, r *http.Request) {
	devID := r.PathValue("id")
	if devID == "" {
		http.Error(w, `{"error":"bad_request","message":"Device ID required"}`, http.StatusBadRequest)
		return
	}

	if s.Registry == nil {
		http.Error(w, `{"error":"server_error","message":"Registry not initialized"}`, http.StatusInternalServerError)
		return
	}

	grants := s.Registry.ListGrants(devID)
	if grants == nil {
		grants = []*device.DeviceGrant{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"device_id": devID,
		"grants":    grants,
	})
}

func (s *Server) putDeviceGrant(w http.ResponseWriter, r *http.Request) {
	devID := r.PathValue("id")
	targetUserID := r.PathValue("user_id")
	if devID == "" || targetUserID == "" {
		http.Error(w, `{"error":"bad_request","message":"Device ID and User ID required"}`, http.StatusBadRequest)
		return
	}

	actor := auth.GetActorFromContext(r.Context())
	grantedBy := ""
	if actor != nil {
		grantedBy = actor.UserID
	}

	defer r.Body.Close()
	var req setGrantReq
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(&req); err != nil {
		http.Error(w, `{"error":"bad_request","message":"Invalid JSON request body"}`, http.StatusBadRequest)
		return
	}

	level := device.GrantLevel(strings.TrimSpace(req.Level))
	if !device.IsValidGrantLevel(level) {
		http.Error(w, `{"error":"bad_request","message":"Invalid grant level: must be read, operate, or manage"}`, http.StatusBadRequest)
		return
	}

	if s.SessionManager != nil {
		if _, err := s.SessionManager.GetUser(targetUserID); err != nil {
			http.Error(w, `{"error":"bad_request","message":"Target user does not exist"}`, http.StatusBadRequest)
			return
		}
	}

	g, err := s.Registry.SetGrant(devID, targetUserID, level, grantedBy)
	if err != nil {
		statusError(w, err)
		return
	}

	if s.Log != nil {
		s.Log.Info("device_grant_set", "device_id", devID, "user_id", targetUserID, "level", level, "granted_by", grantedBy)
	}
	s.recordAudit(r, auth.AuditEvent{
		ActorUserID:  grantedBy,
		Action:       auth.ActionDeviceGrant,
		ResourceType: "device_grant",
		ResourceID:   devID + ":" + targetUserID,
		Status:       "success",
		Detail:       "granted level: " + string(level),
	})

	writeJSON(w, http.StatusOK, g)
}

func (s *Server) deleteDeviceGrant(w http.ResponseWriter, r *http.Request) {
	devID := r.PathValue("id")
	targetUserID := r.PathValue("user_id")
	if devID == "" || targetUserID == "" {
		http.Error(w, `{"error":"bad_request","message":"Device ID and User ID required"}`, http.StatusBadRequest)
		return
	}

	if err := s.Registry.RevokeGrant(devID, targetUserID); err != nil {
		statusError(w, err)
		return
	}

	if s.Log != nil {
		s.Log.Info("device_grant_revoked", "device_id", devID, "user_id", targetUserID)
	}

	actor := auth.GetActorFromContext(r.Context())
	actorID := ""
	if actor != nil {
		actorID = actor.UserID
	}
	s.recordAudit(r, auth.AuditEvent{
		ActorUserID:  actorID,
		Action:       auth.ActionDeviceRevokeGrant,
		ResourceType: "device_grant",
		ResourceID:   devID + ":" + targetUserID,
		Status:       "success",
		Detail:       "revoked device grant",
	})

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) transferDevice(w http.ResponseWriter, r *http.Request) {
	devID := r.PathValue("id")
	if devID == "" {
		http.Error(w, `{"error":"bad_request","message":"Device ID required"}`, http.StatusBadRequest)
		return
	}

	actor := auth.GetActorFromContext(r.Context())
	actorID := ""
	if actor != nil {
		actorID = actor.UserID
	}

	defer r.Body.Close()
	var req transferDeviceReq
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(&req); err != nil {
		http.Error(w, `{"error":"bad_request","message":"Invalid JSON request body"}`, http.StatusBadRequest)
		return
	}

	req.NewOwnerID = strings.TrimSpace(req.NewOwnerID)
	if req.NewOwnerID == "" {
		http.Error(w, `{"error":"bad_request","message":"New owner user ID required"}`, http.StatusBadRequest)
		return
	}

	if s.SessionManager != nil {
		targetUser, err := s.SessionManager.GetUser(req.NewOwnerID)
		if err != nil || targetUser.Status != auth.UserStatusActive {
			http.Error(w, `{"error":"bad_request","message":"New owner must be an active user"}`, http.StatusBadRequest)
			return
		}
	}

	if err := s.Registry.TransferOwnership(devID, req.NewOwnerID, actorID, req.RetainGrant); err != nil {
		statusError(w, err)
		return
	}

	if s.Log != nil {
		s.Log.Info("device_ownership_transferred", "device_id", devID, "new_owner", req.NewOwnerID, "transferred_by", actorID)
	}

	s.recordAudit(r, auth.AuditEvent{
		ActorUserID:  actorID,
		Action:       auth.ActionDeviceTransfer,
		ResourceType: "device",
		ResourceID:   devID,
		Status:       "success",
		Detail:       "transferred ownership to: " + req.NewOwnerID,
	})

	updated, _ := s.Registry.Get(devID)
	writeJSON(w, http.StatusOK, s.toDeviceDTO(updated))
}
