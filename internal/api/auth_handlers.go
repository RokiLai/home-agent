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

	if err := s.SessionManager.AuthenticateAdmin(req.Username, req.Password); err != nil {
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

	rawToken, _, err := s.SessionManager.CreateSession(req.Username, "admin", req.RememberMe)
	if err != nil {
		if s.Log != nil {
			s.Log.Error("create_session_failed", "error", err)
		}
		http.Error(w, `{"error":"server_error","message":"Failed to establish session"}`, http.StatusInternalServerError)
		return
	}

	auth.SetSessionCookie(w, rawToken, req.RememberMe, s.isHTTPS(r))

	if s.Log != nil {
		s.Log.Info("admin_login_success", "username", req.Username, "ip", clientIP, "remember_me", req.RememberMe)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"user": map[string]string{
			"username": req.Username,
			"role":     "admin",
		},
	})
}

func (s *Server) authMe(w http.ResponseWriter, r *http.Request) {
	session := auth.GetSessionFromContext(r.Context())
	if session == nil {
		// 尝试直接通过 Cookie 校验
		rawToken := auth.ExtractSessionToken(r)
		if rawToken != "" && s.SessionManager != nil {
			if sess, err := s.SessionManager.ValidateSession(rawToken); err == nil {
				session = sess
			}
		}
	}

	if session == nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"authenticated": false,
		})
		return
	}

	effectivePublicURL := s.PublicURL
	if effectivePublicURL == "" {
		effectivePublicURL = "https://homeagent.rokilai.online"
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated": true,
		"username":      session.Username,
		"role":          session.Role,
		"public_url":    effectivePublicURL,
	})
}

func (s *Server) getConfig(w http.ResponseWriter, _ *http.Request) {
	effectivePublicURL := s.PublicURL
	if effectivePublicURL == "" {
		effectivePublicURL = "https://homeagent.rokilai.online"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"public_url":  effectivePublicURL,
		"version":     version.Get(),
		"github_repo": "RokiLai/home-agent",
	})
}

func (s *Server) authLogout(w http.ResponseWriter, r *http.Request) {
	rawToken := auth.ExtractSessionToken(r)
	if rawToken != "" && s.SessionManager != nil {
		_ = s.SessionManager.RevokeSession(rawToken)
	}

	auth.ClearSessionCookie(w, s.isHTTPS(r))

	if s.Log != nil {
		s.Log.Info("admin_logout")
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
		http.Error(w, `{"error":"unauthorized","message":"Admin session required"}`, http.StatusUnauthorized)
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

	if err := s.SessionManager.UpdateAdminPassword(session.Username, req.OldPassword, req.NewPassword); err != nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error":   "bad_request",
			"message": err.Error(),
		})
		return
	}

	if s.Log != nil {
		s.Log.Info("admin_password_updated", "username", session.Username)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": "Password updated successfully",
	})
}
