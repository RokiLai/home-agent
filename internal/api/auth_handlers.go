package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"homeagent/internal/auth"
	"homeagent/internal/version"
)

type loginRequest struct {
	Username   string `json:"username"`
	Password   string `json:"password"`
	RememberMe bool   `json:"remember_me"`
}

func (s *Server) isHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func (s *Server) authLogin(w http.ResponseWriter, r *http.Request) {
	clientIP := auth.ExtractClientIP(r)
	if s.RateLimiter != nil {
		if locked, remaining := s.RateLimiter.IsLocked(clientIP); locked {
			if s.Log != nil {
				s.Log.Warn("login_rate_limited", "ip", clientIP, "remaining_seconds", int(remaining.Seconds()))
			}
			w.Header().Set("Retry-After", time.Duration(remaining.Seconds()).String())
			http.Error(w, `{"error":"too_many_requests","message":"Account temporarily locked due to consecutive failed attempts"}`, http.StatusTooManyRequests)
			return
		}
	}

	defer r.Body.Close()
	var req loginRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(&req); err != nil {
		http.Error(w, `{"error":"bad_request","message":"Invalid JSON request body"}`, http.StatusBadRequest)
		return
	}

	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" || req.Password == "" {
		http.Error(w, `{"error":"bad_request","message":"Username and password required"}`, http.StatusBadRequest)
		return
	}

	if s.SessionManager == nil {
		http.Error(w, `{"error":"server_error","message":"Auth session manager not initialized"}`, http.StatusInternalServerError)
		return
	}

	user, err := s.SessionManager.AuthenticateUser(req.Username, req.Password)
	if err != nil {
		if s.RateLimiter != nil {
			locked, lockDur := s.RateLimiter.RecordFailure(clientIP)
			if locked && s.Log != nil {
				s.Log.Warn("ip_lockout_triggered", "ip", clientIP, "duration", lockDur.String())
			}
		}
		if s.Log != nil {
			s.Log.Warn("login_failed", "ip", clientIP, "username", req.Username)
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error":   "unauthorized",
			"message": "Invalid username or password",
		})
		return
	}

	if s.RateLimiter != nil {
		s.RateLimiter.RecordSuccess(clientIP)
	}

	rawToken, session, err := s.SessionManager.CreateUserSession(user.ID, req.RememberMe)
	if err != nil {
		if s.Log != nil {
			s.Log.Error("create_session_failed", "error", err)
		}
		http.Error(w, `{"error":"server_error","message":"Failed to establish session"}`, http.StatusInternalServerError)
		return
	}

	auth.SetSessionCookie(w, rawToken, req.RememberMe, s.isHTTPS(r))

	if s.Log != nil {
		s.Log.Info("user_login_success", "user_id", user.ID, "username", user.Username, "role", user.Role, "ip", clientIP, "remember_me", req.RememberMe)
	}
	s.recordAudit(r, auth.AuditEvent{
		ActorUserID:  user.ID,
		ActorRole:    user.Role,
		Action:       auth.ActionAuthLogin,
		ResourceType: "user",
		ResourceID:   user.ID,
		ClientIP:     clientIP,
		Status:       "success",
		Detail:       "user logged in successfully",
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"user": map[string]any{
			"id":       user.ID,
			"username": user.Username,
			"role":     user.Role,
		},
		"session": map[string]any{
			"remember_me": session.RememberMe,
			"expires_at":  session.ExpiresAt,
		},
	})
}

func (s *Server) authMe(w http.ResponseWriter, r *http.Request) {
	session := auth.GetSessionFromContext(r.Context())
	if session == nil {
		rawToken := auth.ExtractSessionToken(r)
		if rawToken != "" && s.SessionManager != nil {
			if sess, err := s.SessionManager.ValidateSession(rawToken); err == nil {
				session = sess
			}
		}
	}
	if session == nil {
		http.Error(w, `{"error":"unauthorized","message":"User session required"}`, http.StatusUnauthorized)
		return
	}

	effectivePublicURL := s.PublicURL
	if effectivePublicURL == "" {
		effectivePublicURL = "https://homeagent.rokilai.online"
	}

	role := auth.Role(session.Role)
	perms := auth.PermissionsForRole(role)

	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated": true,
		"user_id":       session.UserID,
		"username":      session.Username,
		"role":          session.Role,
		"permissions":   perms,
		"public_url":    effectivePublicURL,
		"version":       version.Get(),
		"github_repo":   "RokiLai/home-agent",
	})
}

func (s *Server) getConfig(w http.ResponseWriter, _ *http.Request) {
	effectivePublicURL := s.PublicURL
	if effectivePublicURL == "" {
		effectivePublicURL = "https://homeagent.rokilai.online"
	}
	repo := s.GitHubRepo
	if repo == "" {
		repo = "RokiLai/home-agent"
	}
	upgradeSource := s.UpgradeSource
	if upgradeSource == "" {
		upgradeSource = "github"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"public_url":           effectivePublicURL,
		"version":              version.Get(),
		"github_repo":          repo,
		"upgrade_source":       upgradeSource,
		"github_mirror_prefix": s.GitHubMirrorPrefix,
	})
}

func (s *Server) authLogout(w http.ResponseWriter, r *http.Request) {
	session := auth.GetSessionFromContext(r.Context())
	rawToken := auth.ExtractSessionToken(r)
	if rawToken != "" && s.SessionManager != nil {
		_ = s.SessionManager.RevokeSession(rawToken)
	}

	auth.ClearSessionCookie(w, s.isHTTPS(r))

	if s.Log != nil {
		s.Log.Info("user_logout")
	}

	if session != nil {
		s.recordAudit(r, auth.AuditEvent{
			ActorUserID:  session.UserID,
			ActorRole:    auth.Role(session.Role),
			Action:       auth.ActionAuthLogout,
			ResourceType: "session",
			ResourceID:   session.UserID,
			Status:       "success",
			Detail:       "user logged out",
		})
	}

	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

type changePasswordRequest struct {
	OldPassword string `json:"old_password"`
	NewPassword string `json:"new_password"`
}

func (s *Server) authChangePassword(w http.ResponseWriter, r *http.Request) {
	session := auth.GetSessionFromContext(r.Context())
	if session == nil {
		rawToken := auth.ExtractSessionToken(r)
		if rawToken != "" && s.SessionManager != nil {
			if sess, err := s.SessionManager.ValidateSession(rawToken); err == nil {
				session = sess
			}
		}
	}
	if session == nil {
		http.Error(w, `{"error":"unauthorized","message":"User session required"}`, http.StatusUnauthorized)
		return
	}

	defer r.Body.Close()
	var req changePasswordRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(&req); err != nil {
		http.Error(w, `{"error":"bad_request","message":"Invalid JSON request body"}`, http.StatusBadRequest)
		return
	}

	if strings.TrimSpace(req.OldPassword) == "" || strings.TrimSpace(req.NewPassword) == "" {
		http.Error(w, `{"error":"bad_request","message":"Old password and new password required"}`, http.StatusBadRequest)
		return
	}

	if s.SessionManager == nil {
		http.Error(w, `{"error":"server_error","message":"Auth session manager not initialized"}`, http.StatusInternalServerError)
		return
	}

	if err := s.SessionManager.UpdateUserPassword(session.UserID, req.OldPassword, req.NewPassword); err != nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error":   "bad_request",
			"message": err.Error(),
		})
		return
	}

	if s.Log != nil {
		s.Log.Info("user_password_updated", "user_id", session.UserID, "username", session.Username)
	}
	s.recordAudit(r, auth.AuditEvent{
		ActorUserID:  session.UserID,
		ActorRole:    auth.Role(session.Role),
		Action:       auth.ActionAuthChangePass,
		ResourceType: "user",
		ResourceID:   session.UserID,
		Status:       "success",
		Detail:       "password changed by user",
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": "Password updated successfully",
	})
}

func (s *Server) authLogoutAll(w http.ResponseWriter, r *http.Request) {
	session := auth.GetSessionFromContext(r.Context())
	if session == nil {
		http.Error(w, `{"error":"unauthorized","message":"User session required"}`, http.StatusUnauthorized)
		return
	}

	if s.SessionManager != nil {
		_ = s.SessionManager.RevokeUserSessions(session.UserID)
	}

	auth.ClearSessionCookie(w, s.isHTTPS(r))

	s.recordAudit(r, auth.AuditEvent{
		ActorUserID:  session.UserID,
		ActorRole:    auth.Role(session.Role),
		Action:       auth.ActionAuthLogoutAll,
		ResourceType: "session",
		ResourceID:   session.UserID,
		Status:       "success",
		Detail:       "revoked all user sessions",
	})

	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func (s *Server) handleListAuditLogs(w http.ResponseWriter, r *http.Request) {
	if s.AuditLogger == nil {
		s.AuditLogger = auth.NewMemoryAuditLogger(500)
	}

	events := s.AuditLogger.Recent(100)
	writeJSON(w, http.StatusOK, map[string]any{
		"events": events,
		"count":  len(events),
	})
}

func (s *Server) recordAudit(r *http.Request, event auth.AuditEvent) {
	if s.AuditLogger == nil {
		s.AuditLogger = auth.NewMemoryAuditLogger(500)
	}
	if event.ClientIP == "" && r != nil {
		event.ClientIP = auth.ExtractClientIP(r)
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	s.AuditLogger.Record(event)
}
