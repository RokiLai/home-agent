package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"homeagent/internal/auth"
	"homeagent/internal/device"
	"homeagent/internal/registry"
	"homeagent/internal/sshsync"
)

type Server struct {
	Registry              *registry.Registry
	Token, AdminPublicKey string
	Sync                  *sshsync.Controller
	Log                   *slog.Logger
	DownloadsDir          string
	ScriptsDir            string
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	if s.DownloadsDir != "" {
		mux.Handle("GET /downloads/", http.StripPrefix("/downloads/", http.FileServer(http.Dir(s.DownloadsDir))))
	}
	if s.ScriptsDir != "" {
		mux.HandleFunc("GET /install.sh", func(w http.ResponseWriter, r *http.Request) { http.ServeFile(w, r, s.ScriptsDir+"/install.sh") })
		mux.HandleFunc("GET /install.ps1", func(w http.ResponseWriter, r *http.Request) { http.ServeFile(w, r, s.ScriptsDir+"/install.ps1") })
	}
	mux.Handle("GET /api/v1/bootstrap/admin-key", auth.Bearer(s.Token, http.HandlerFunc(s.adminKey)))
	mux.Handle("POST /api/v1/devices/register", auth.Bearer(s.Token, http.HandlerFunc(s.register)))
	mux.Handle("GET /api/v1/devices", auth.Bearer(s.Token, http.HandlerFunc(s.devices)))
	mux.Handle("GET /api/v1/devices/{id}", auth.Bearer(s.Token, http.HandlerFunc(s.getDevice)))
	mux.Handle("DELETE /api/v1/devices/{id}", auth.Bearer(s.Token, http.HandlerFunc(s.deleteDevice)))
	mux.Handle("POST /api/v1/devices/{id}/sync", auth.Bearer(s.Token, http.HandlerFunc(s.syncDevice)))
	mux.Handle("POST /api/v1/sync", auth.Bearer(s.Token, http.HandlerFunc(s.syncAll)))
	return mux
}

func (s *Server) adminKey(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]string{"public_key": s.AdminPublicKey})
}
func (s *Server) devices(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{"devices": s.Registry.List()})
}
func (s *Server) getDevice(w http.ResponseWriter, r *http.Request) {
	d, err := s.Registry.Get(r.PathValue("id"))
	if err != nil {
		statusError(w, err)
		return
	}
	writeJSON(w, 200, d)
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	var d device.Device
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		http.Error(w, "invalid JSON request", 400)
		return
	}
	saved, err := s.Registry.Save(d)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if s.Log != nil {
		s.Log.Info("device_registered", "device_id", saved.ID, "hostname", saved.Hostname)
	}
	if s.Sync != nil {
		go s.Sync.SyncAll(context.Background())
	}
	writeJSON(w, 200, map[string]any{"success": true, "device_id": saved.ID})
}

func (s *Server) deleteDevice(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.Registry.Delete(id); err != nil {
		statusError(w, err)
		return
	}
	if s.Sync != nil {
		go s.Sync.SyncAll(context.Background())
	}
	w.WriteHeader(204)
}
func (s *Server) syncDevice(w http.ResponseWriter, r *http.Request) {
	if s.Sync == nil {
		http.Error(w, "sync disabled", 503)
		return
	}
	writeJSON(w, 200, s.Sync.SyncDevice(r.Context(), r.PathValue("id")))
}
func (s *Server) syncAll(w http.ResponseWriter, r *http.Request) {
	if s.Sync == nil {
		http.Error(w, "sync disabled", 503)
		return
	}
	writeJSON(w, 200, map[string]any{"results": s.Sync.SyncAll(r.Context())})
}

func statusError(w http.ResponseWriter, err error) {
	if errors.Is(err, registry.ErrNotFound) {
		http.Error(w, "not found", 404)
	} else {
		http.Error(w, "internal error", 500)
	}
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func RedactedRequest(r *http.Request) string {
	return r.Method + " " + r.URL.Path + " authorization=" + strings.Repeat("*", len(r.Header.Get("Authorization")))
}
