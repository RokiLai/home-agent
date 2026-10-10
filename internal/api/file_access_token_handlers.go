package api

import (
	"context"
	"encoding/json"
	"errors"
	"homeagent/internal/auth"
	"homeagent/internal/fileaccess"
	"io"
	"net/http"
	"strings"
)

type fileCredentialKey struct{}
type fileCredential struct {
	raw    string
	userID string
}

func fileTokenUserLookup(sm *auth.SessionManager) func(string) (fileaccess.User, error) {
	return func(id string) (fileaccess.User, error) {
		u, err := sm.GetUser(id)
		if errors.Is(err, auth.ErrUserNotFound) {
			return fileaccess.User{}, fileaccess.ErrNotFound
		}
		if err != nil {
			return fileaccess.User{}, fileaccess.ErrUnavailable
		}
		return fileaccess.User{ID: u.ID, Version: u.SessionVersion, Active: u.Status == auth.UserStatusActive}, nil
	}
}
func fileActorID(r *http.Request) string {
	if c, ok := r.Context().Value(fileCredentialKey{}).(fileCredential); ok {
		return c.userID
	}
	if a := auth.GetActorFromContext(r.Context()); a != nil {
		return a.UserID
	}
	return ""
}
func (s *Server) fileCommitGuard(r *http.Request) func(func() error) error {
	c, ok := r.Context().Value(fileCredentialKey{}).(fileCredential)
	if !ok {
		return nil
	}
	return func(commit func() error) error { return s.FileAccess.Guard(c.raw, commit) }
}
func (s *Server) fileCredentialError(w http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	label := "server_error"
	switch {
	case errors.Is(err, fileaccess.ErrUnauthorized):
		code = 401
		label = "unauthorized"
	case errors.Is(err, fileaccess.ErrUnavailable):
		code = 503
		label = "file_token_service_unavailable"
	case errors.Is(err, fileaccess.ErrInvalid):
		code = 400
		label = "invalid_request"
	case errors.Is(err, fileaccess.ErrNotFound):
		code = 404
		label = "not_found"
	case errors.Is(err, fileaccess.ErrLimit):
		code = 409
		label = "token_limit_reached"
	}
	writeJSON(w, code, map[string]string{"error": label})
}
func (s *Server) fileCredentialBoundary(mux *http.ServeMux) http.Handler {
	allowed := map[string]bool{"POST /api/v1/files": true, "GET /api/v1/files": true, "GET /api/v1/files/{id}/download": true, "POST /api/v1/files/{id}/links": true}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			mux.ServeHTTP(w, r)
			return
		}
		raw := ""
		for _, v := range r.Header.Values("Authorization") {
			if strings.Contains(v, "agt_file_") {
				if raw != "" || !strings.HasPrefix(v, "Bearer agt_file_") {
					s.fileCredentialError(w, fileaccess.ErrUnauthorized)
					return
				}
				raw = strings.TrimPrefix(v, "Bearer ")
			}
		}
		for _, values := range r.URL.Query() {
			for _, value := range values {
				if strings.HasPrefix(value, "agt_file_") {
					s.fileCredentialError(w, fileaccess.ErrUnauthorized)
					return
				}
			}
		}
		for _, c := range r.Cookies() {
			if strings.HasPrefix(c.Value, "agt_file_") {
				s.fileCredentialError(w, fileaccess.ErrUnauthorized)
				return
			}
		}
		if raw == "" {
			mux.ServeHTTP(w, r)
			return
		}
		if s.FileAccess == nil {
			s.fileCredentialError(w, fileaccess.ErrUnavailable)
			return
		}
		user, err := s.FileAccess.Validate(raw)
		if err != nil {
			s.fileCredentialError(w, err)
			return
		}
		_, pattern := mux.Handler(r)
		if !allowed[pattern] {
			writeJSON(w, 403, map[string]string{"error": "forbidden"})
			return
		}
		mux.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), fileCredentialKey{}, fileCredential{raw: raw, userID: user.ID})))
	})
}
func (s *Server) createFileAccessToken(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.FileAccess == nil {
		s.fileCredentialError(w, fileaccess.ErrUnavailable)
		return
	}
	var request struct {
		Name string `json:"name"`
		Days *int   `json:"expires_in_days"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if dec.Decode(&request) != nil || dec.Decode(&struct{}{}) != io.EOF {
		s.fileCredentialError(w, fileaccess.ErrInvalid)
		return
	}
	days := 365
	if request.Days != nil {
		days = *request.Days
		if days == 0 {
			s.fileCredentialError(w, fileaccess.ErrInvalid)
			return
		}
	}
	rec, raw, err := s.FileAccess.Create(fileActorID(r), request.Name, days)
	if err != nil {
		s.fileCredentialError(w, err)
		return
	}
	s.recordAudit(r, auth.AuditEvent{ActorUserID: fileActorID(r), Action: "file_token.create", ResourceType: "file_token", ResourceID: rec.ID, Status: "success"})
	writeJSON(w, 201, struct {
		fileaccess.Record
		Token string `json:"token"`
	}{rec, raw})
}
func (s *Server) listFileAccessTokens(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.FileAccess == nil {
		s.fileCredentialError(w, fileaccess.ErrUnavailable)
		return
	}
	tokens, err := s.FileAccess.List(fileActorID(r))
	if err != nil {
		s.fileCredentialError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"tokens": tokens})
}
func (s *Server) revokeFileAccessToken(w http.ResponseWriter, r *http.Request) {
	if s.FileAccess == nil {
		s.fileCredentialError(w, fileaccess.ErrUnavailable)
		return
	}
	if err := s.FileAccess.Revoke(fileActorID(r), r.PathValue("id")); err != nil {
		s.fileCredentialError(w, err)
		return
	}
	s.recordAudit(r, auth.AuditEvent{ActorUserID: fileActorID(r), Action: "file_token.revoke", ResourceType: "file_token", ResourceID: r.PathValue("id"), Status: "success"})
	w.WriteHeader(204)
}
