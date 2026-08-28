package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"homeagent/internal/auth"
)

type createUserReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Role     string `json:"role"`
}

type updateUserRoleReq struct {
	Role string `json:"role"`
}

type resetPasswordReq struct {
	NewPassword string `json:"new_password"`
}

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	if s.SessionManager == nil {
		http.Error(w, `{"error":"server_error","message":"Session manager not initialized"}`, http.StatusInternalServerError)
		return
	}

	actor := auth.GetActorFromContext(r.Context())
	if actor == nil {
		http.Error(w, `{"error":"unauthorized","message":"Valid session required"}`, http.StatusUnauthorized)
		return
	}

	// Owner 查看全量用户列表；非 Owner 仅查看自身摘要
	if actor.Role == auth.RoleOwner {
		users := s.SessionManager.ListUsers()
		writeJSON(w, http.StatusOK, map[string]any{"users": users})
		return
	}

	u, err := s.SessionManager.GetUser(actor.UserID)
	if err != nil {
		statusError(w, err)
		return
	}
	u.PasswordHash = ""
	writeJSON(w, http.StatusOK, map[string]any{"users": []*auth.User{u}})
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	if s.SessionManager == nil {
		http.Error(w, `{"error":"server_error","message":"Session manager not initialized"}`, http.StatusInternalServerError)
		return
	}

	actor := auth.GetActorFromContext(r.Context())
	createdBy := ""
	if actor != nil {
		createdBy = actor.UserID
	}

	defer r.Body.Close()
	var req createUserReq
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(&req); err != nil {
		http.Error(w, `{"error":"bad_request","message":"Invalid JSON request"}`, http.StatusBadRequest)
		return
	}

	role := auth.Role(strings.TrimSpace(req.Role))
	if !auth.IsValidRole(role) {
		http.Error(w, `{"error":"bad_request","message":"Invalid role: must be owner, admin, or viewer"}`, http.StatusBadRequest)
		return
	}

	created, err := s.SessionManager.CreateUser(req.Username, req.Password, role, createdBy)
	if err != nil {
		if err == auth.ErrUsernameConflict {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error":   "username_conflict",
				"message": "Username already exists",
			})
			return
		}
		http.Error(w, `{"error":"bad_request","message":"`+err.Error()+`"}`, http.StatusBadRequest)
		return
	}

	if s.Log != nil {
		s.Log.Info("user_created", "user_id", created.ID, "username", created.Username, "role", created.Role)
	}
	s.recordAudit(r, auth.AuditEvent{
		ActorUserID:  createdBy,
		Action:       auth.ActionUserCreate,
		ResourceType: "user",
		ResourceID:   created.ID,
		Status:       "success",
		Detail:       "created user: " + created.Username + " with role: " + string(created.Role),
	})

	created.PasswordHash = ""
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) updateUserRole(w http.ResponseWriter, r *http.Request) {
	targetUserID := r.PathValue("id")
	if targetUserID == "" {
		http.Error(w, `{"error":"bad_request","message":"User ID required"}`, http.StatusBadRequest)
		return
	}

	defer r.Body.Close()
	var req updateUserRoleReq
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(&req); err != nil {
		http.Error(w, `{"error":"bad_request","message":"Invalid JSON request"}`, http.StatusBadRequest)
		return
	}

	role := auth.Role(strings.TrimSpace(req.Role))
	if !auth.IsValidRole(role) {
		http.Error(w, `{"error":"bad_request","message":"Invalid role"}`, http.StatusBadRequest)
		return
	}

	if err := s.SessionManager.UpdateUserRole(targetUserID, role); err != nil {
		if err == auth.ErrLastOwnerRequired {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error":   "last_owner_required",
				"message": "Cannot demote the last active owner",
			})
			return
		}
		statusError(w, err)
		return
	}

	actor := auth.GetActorFromContext(r.Context())
	actorID := ""
	if actor != nil {
		actorID = actor.UserID
	}
	s.recordAudit(r, auth.AuditEvent{
		ActorUserID:  actorID,
		Action:       auth.ActionUserUpdateRole,
		ResourceType: "user",
		ResourceID:   targetUserID,
		Status:       "success",
		Detail:       "updated role to: " + string(role),
	})

	updated, _ := s.SessionManager.GetUser(targetUserID)
	updated.PasswordHash = ""
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) disableUser(w http.ResponseWriter, r *http.Request) {
	targetUserID := r.PathValue("id")
	if targetUserID == "" {
		http.Error(w, `{"error":"bad_request","message":"User ID required"}`, http.StatusBadRequest)
		return
	}

	if err := s.SessionManager.DisableUser(targetUserID); err != nil {
		if err == auth.ErrLastOwnerRequired {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error":   "last_owner_required",
				"message": "Cannot disable the last active owner",
			})
			return
		}
		statusError(w, err)
		return
	}

	actor := auth.GetActorFromContext(r.Context())
	actorID := ""
	if actor != nil {
		actorID = actor.UserID
	}
	s.recordAudit(r, auth.AuditEvent{
		ActorUserID:  actorID,
		Action:       auth.ActionUserDisable,
		ResourceType: "user",
		ResourceID:   targetUserID,
		Status:       "success",
		Detail:       "user account disabled",
	})

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) enableUser(w http.ResponseWriter, r *http.Request) {
	targetUserID := r.PathValue("id")
	if targetUserID == "" {
		http.Error(w, `{"error":"bad_request","message":"User ID required"}`, http.StatusBadRequest)
		return
	}

	if err := s.SessionManager.EnableUser(targetUserID); err != nil {
		statusError(w, err)
		return
	}

	actor := auth.GetActorFromContext(r.Context())
	actorID := ""
	if actor != nil {
		actorID = actor.UserID
	}
	s.recordAudit(r, auth.AuditEvent{
		ActorUserID:  actorID,
		Action:       auth.ActionUserEnable,
		ResourceType: "user",
		ResourceID:   targetUserID,
		Status:       "success",
		Detail:       "user account enabled",
	})

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) resetUserPassword(w http.ResponseWriter, r *http.Request) {
	targetUserID := r.PathValue("id")
	if targetUserID == "" {
		http.Error(w, `{"error":"bad_request","message":"User ID required"}`, http.StatusBadRequest)
		return
	}

	defer r.Body.Close()
	var req resetPasswordReq
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(&req); err != nil {
		http.Error(w, `{"error":"bad_request","message":"Invalid JSON request"}`, http.StatusBadRequest)
		return
	}

	if err := s.SessionManager.ResetUserPassword(targetUserID, req.NewPassword); err != nil {
		statusError(w, err)
		return
	}

	actor := auth.GetActorFromContext(r.Context())
	actorID := ""
	if actor != nil {
		actorID = actor.UserID
	}
	s.recordAudit(r, auth.AuditEvent{
		ActorUserID:  actorID,
		Action:       auth.ActionUserResetPass,
		ResourceType: "user",
		ResourceID:   targetUserID,
		Status:       "success",
		Detail:       "user password reset by admin",
	})

	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Password reset successfully"})
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request) {
	targetUserID := r.PathValue("id")
	if targetUserID == "" {
		http.Error(w, `{"error":"bad_request","message":"User ID required"}`, http.StatusBadRequest)
		return
	}

	if err := s.SessionManager.DeleteUser(targetUserID); err != nil {
		if err == auth.ErrLastOwnerRequired {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error":   "last_owner_required",
				"message": "Cannot delete the last active owner",
			})
			return
		}
		statusError(w, err)
		return
	}

	// 决策点 2 契约：级联物理删除该用户拥有的全部设备及授权
	if s.Registry != nil {
		deletedDeviceIDs, _ := s.Registry.DeleteDevicesByOwner(targetUserID)
		if s.Log != nil && len(deletedDeviceIDs) > 0 {
			s.Log.Info("user_deleted_cascade_purged_devices", "user_id", targetUserID, "devices_count", len(deletedDeviceIDs), "device_ids", deletedDeviceIDs)
		}
	}

	actor := auth.GetActorFromContext(r.Context())
	actorID := ""
	if actor != nil {
		actorID = actor.UserID
	}
	s.recordAudit(r, auth.AuditEvent{
		ActorUserID:  actorID,
		Action:       auth.ActionUserDelete,
		ResourceType: "user",
		ResourceID:   targetUserID,
		Status:       "success",
		Detail:       "user deleted with cascading device purge",
	})

	w.WriteHeader(http.StatusNoContent)
}
