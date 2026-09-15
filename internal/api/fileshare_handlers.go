package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"homeagent/internal/auth"
	"homeagent/internal/fileshare"
)

const fileUploadProtocolOverhead int64 = 1 << 20

const (
	actionFileUpload     = "file.upload"
	actionFileDelete     = "file.delete"
	actionFileLinkCreate = "file.link_create"
	actionFileLinkRevoke = "file.link_revoke"
	actionFileSettings   = "file.settings_update"
)

type downloadRateWindow struct {
	mu      sync.Mutex
	started time.Time
	count   int
}

func (s *Server) requireFileShare(w http.ResponseWriter) *fileshare.Service {
	if s.FileShare == nil {
		http.Error(w, `{"error":"file_service_unavailable"}`, http.StatusServiceUnavailable)
		return nil
	}
	return s.FileShare
}

func (s *Server) uploadFile(w http.ResponseWriter, r *http.Request) {
	service := s.requireFileShare(w)
	if service == nil {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, fileshare.DefaultQuotaBytes+fileUploadProtocolOverhead)
	mr, err := r.MultipartReader()
	if err != nil {
		http.Error(w, `{"error":"invalid_multipart"}`, http.StatusBadRequest)
		return
	}
	part, err := mr.NextPart()
	if err != nil || part.FormName() != "file" || part.FileName() == "" {
		http.Error(w, `{"error":"single_file_required"}`, http.StatusBadRequest)
		return
	}
	actor := auth.GetActorFromContext(r.Context())
	userID := ""
	if actor != nil {
		userID = actor.UserID
	}
	record, err := service.Upload(part.FileName(), part.Header.Get("Content-Type"), userID, 0, part)
	_ = part.Close()
	if err != nil {
		s.writeFileError(w, err)
		return
	}
	extra, nextErr := mr.NextPart()
	if nextErr != io.EOF {
		if extra != nil {
			_ = extra.Close()
		}
		_ = service.Delete(record.ID)
		http.Error(w, `{"error":"single_file_required"}`, http.StatusBadRequest)
		return
	}
	s.recordFileAudit(r, actionFileUpload, record.ID, userID, "success")
	writeJSON(w, http.StatusCreated, record)
}

func (s *Server) listFiles(w http.ResponseWriter, _ *http.Request) {
	service := s.requireFileShare(w)
	if service == nil {
		return
	}
	files, usage := service.List()
	writeJSON(w, http.StatusOK, map[string]any{"files": files, "quota_bytes": usage.QuotaBytes, "used_bytes": usage.UsedBytes, "reserved_bytes": usage.ReservedBytes})
}

func (s *Server) downloadFile(w http.ResponseWriter, r *http.Request) {
	service := s.requireFileShare(w)
	if service == nil {
		return
	}
	f, record, err := service.OpenFile(r.PathValue("id"))
	if err != nil {
		s.writeFileError(w, err)
		return
	}
	defer f.Close()
	s.serveDownload(w, r, f, record)
}

func (s *Server) deleteFile(w http.ResponseWriter, r *http.Request) {
	service := s.requireFileShare(w)
	if service == nil {
		return
	}
	id := r.PathValue("id")
	if err := service.Delete(id); err != nil {
		s.writeFileError(w, err)
		return
	}
	actor := auth.GetActorFromContext(r.Context())
	actorID := ""
	if actor != nil {
		actorID = actor.UserID
	}
	s.recordFileAudit(r, actionFileDelete, id, actorID, "success")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) createFileLink(w http.ResponseWriter, r *http.Request) {
	service := s.requireFileShare(w)
	if service == nil {
		return
	}
	var req struct {
		ExpiresInSeconds int64 `json:"expires_in_seconds"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid_request"}`, http.StatusBadRequest)
		return
	}
	actor := auth.GetActorFromContext(r.Context())
	actorID := ""
	if actor != nil {
		actorID = actor.UserID
	}
	link, token, err := service.CreateLink(r.PathValue("id"), actorID, time.Duration(req.ExpiresInSeconds)*time.Second)
	if err != nil {
		s.writeFileError(w, err)
		return
	}
	base := strings.TrimRight(s.PublicURL, "/")
	if base == "" {
		base = "https://homeagent.rokilai.online"
	}
	downloadURL := fmt.Sprintf("%s/api/v1/public/files/%s/download?token=%s", base, url.PathEscape(link.FileID), url.QueryEscape(token))
	s.recordFileAudit(r, actionFileLinkCreate, link.ID, actorID, "success")
	writeJSON(w, http.StatusCreated, map[string]any{"id": link.ID, "download_url": downloadURL, "expires_at": link.ExpiresAt})
}

func (s *Server) listFileLinks(w http.ResponseWriter, r *http.Request) {
	service := s.requireFileShare(w)
	if service == nil {
		return
	}
	links, err := service.ListLinks(r.PathValue("id"))
	if err != nil {
		s.writeFileError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"links": links})
}

func (s *Server) revokeFileLink(w http.ResponseWriter, r *http.Request) {
	service := s.requireFileShare(w)
	if service == nil {
		return
	}
	if err := service.RevokeLink(r.PathValue("id"), r.PathValue("link_id")); err != nil {
		s.writeFileError(w, err)
		return
	}
	actor := auth.GetActorFromContext(r.Context())
	actorID := ""
	if actor != nil {
		actorID = actor.UserID
	}
	s.recordFileAudit(r, actionFileLinkRevoke, r.PathValue("link_id"), actorID, "success")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) getFileSettings(w http.ResponseWriter, _ *http.Request) {
	service := s.requireFileShare(w)
	if service == nil {
		return
	}
	_, usage := service.List()
	writeJSON(w, http.StatusOK, map[string]any{"retention_days": service.Settings().RetentionDays, "revision": service.Settings().Revision, "quota_bytes": usage.QuotaBytes, "used_bytes": usage.UsedBytes, "reserved_bytes": usage.ReservedBytes})
}

func (s *Server) putFileSettings(w http.ResponseWriter, r *http.Request) {
	service := s.requireFileShare(w)
	if service == nil {
		return
	}
	actor := auth.GetActorFromContext(r.Context())
	if actor == nil || !auth.RoleHasPermission(actor.Role, auth.PermInstanceSettingsManage) {
		http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
		return
	}
	var next fileshare.Settings
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&next); err != nil {
		http.Error(w, `{"error":"invalid_request"}`, http.StatusBadRequest)
		return
	}
	updated, err := service.UpdateSettings(next)
	if err != nil {
		s.writeFileError(w, err)
		return
	}
	actorID := actor.UserID
	s.recordFileAudit(r, actionFileSettings, "global", actorID, "success")
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) downloadPublicFile(w http.ResponseWriter, r *http.Request) {
	service := s.requireFileShare(w)
	if service == nil {
		return
	}
	if !s.allowPublicDownload(auth.ExtractClientIP(r)) {
		http.Error(w, `{"error":"too_many_requests"}`, http.StatusTooManyRequests)
		return
	}
	select {
	case s.fileDownloadSlots <- struct{}{}:
		defer func() { <-s.fileDownloadSlots }()
	default:
		http.Error(w, `{"error":"too_many_requests"}`, http.StatusTooManyRequests)
		return
	}
	f, record, err := service.OpenPublicFile(r.PathValue("id"), r.URL.Query().Get("token"))
	if err != nil {
		s.writeFileError(w, err)
		return
	}
	defer f.Close()
	s.serveDownload(w, r, f, record)
}

func (s *Server) allowPublicDownload(ip string) bool {
	s.fileShareOnce.Do(func() { s.fileDownloadSlots = make(chan struct{}, 16) })
	value, _ := s.fileDownloadRates.LoadOrStore(ip, &downloadRateWindow{started: time.Now()})
	window := value.(*downloadRateWindow)
	window.mu.Lock()
	defer window.mu.Unlock()
	now := time.Now()
	if now.Sub(window.started) >= time.Minute {
		window.started = now
		window.count = 0
	}
	if window.count >= 60 {
		return false
	}
	window.count++
	return true
}

func (s *Server) serveDownload(w http.ResponseWriter, r *http.Request, f http.File, record fileshare.FileRecord) {
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": record.Name})
	w.Header().Set("Content-Disposition", disposition)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.ServeContent(w, r, path.Base(record.Name), record.CreatedAt, f)
}

func (s *Server) writeFileError(w http.ResponseWriter, err error) {
	status, code := http.StatusInternalServerError, "file_operation_failed"
	switch {
	case errors.Is(err, fileshare.ErrNotFound):
		status, code = http.StatusNotFound, "not_found"
	case errors.Is(err, fileshare.ErrGone):
		status, code = http.StatusGone, "gone"
	case errors.Is(err, fileshare.ErrConflict):
		status, code = http.StatusConflict, "revision_conflict"
	case errors.Is(err, fileshare.ErrInsufficientStorage):
		status, code = http.StatusInsufficientStorage, "insufficient_storage"
	case errors.Is(err, fileshare.ErrInvalidName), errors.Is(err, fileshare.ErrInvalidSettings), errors.Is(err, fileshare.ErrInvalidDuration):
		status, code = http.StatusBadRequest, "invalid_request"
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
}

func (s *Server) recordFileAudit(r *http.Request, action, resourceID, actorID, status string) {
	actor := auth.GetActorFromContext(r.Context())
	role := auth.Role("")
	if actor != nil {
		role = actor.Role
	}
	s.recordAudit(r, auth.AuditEvent{ActorUserID: actorID, ActorRole: role, Action: action, ResourceType: "file", ResourceID: resourceID, Status: status})
}
