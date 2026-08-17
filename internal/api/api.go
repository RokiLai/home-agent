package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"homeagent/internal/acl"
	"homeagent/internal/auth"
	"homeagent/internal/broker"
	"homeagent/internal/device"
	"homeagent/internal/registry"
	"homeagent/internal/sshsync"
	"homeagent/internal/ui"
)

type Server struct {
	Registry              *registry.Registry
	Broker                *broker.Broker
	ACLPath               string
	Token, AdminPublicKey string
	Sync                  *sshsync.Controller
	Log                   *slog.Logger
	DownloadsDir          string
	ScriptsDir            string
	PingInterval          time.Duration

	version int64
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

	// Web Dashboard UI routes
	mux.Handle("GET /static/", http.StripPrefix("/static/", ui.Handler()))
	mux.HandleFunc("GET /{$}", s.indexPage)
	mux.HandleFunc("GET /dashboard", s.indexPage)

	mux.Handle("GET /api/v1/bootstrap/admin-key", auth.Bearer(s.Token, http.HandlerFunc(s.adminKey)))
	mux.Handle("POST /api/v1/devices/register", auth.Bearer(s.Token, http.HandlerFunc(s.register)))
	mux.Handle("GET /api/v1/devices", auth.Bearer(s.Token, http.HandlerFunc(s.devices)))
	mux.Handle("GET /api/v1/devices/{id}", auth.Bearer(s.Token, http.HandlerFunc(s.getDevice)))
	mux.Handle("DELETE /api/v1/devices/{id}", auth.Bearer(s.Token, http.HandlerFunc(s.deleteDevice)))
	mux.Handle("POST /api/v1/devices/{id}/sync", auth.Bearer(s.Token, http.HandlerFunc(s.syncDevice)))
	mux.Handle("POST /api/v1/sync", auth.Bearer(s.Token, http.HandlerFunc(s.syncAll)))

	// SSE and Control Plane routes
	mux.Handle("GET /api/v1/devices/{id}/events", auth.Bearer(s.Token, http.HandlerFunc(s.deviceEvents)))
	mux.Handle("POST /api/v1/devices/{id}/ack", auth.Bearer(s.Token, http.HandlerFunc(s.deviceAck)))
	mux.Handle("GET /api/v1/devices/{id}/keys", auth.Bearer(s.Token, http.HandlerFunc(s.deviceKeys)))

	return mux
}

func (s *Server) indexPage(w http.ResponseWriter, _ *http.Request) {
	content, err := ui.GetIndexHTML()
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(content)
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
	if s.Broker != nil {
		s.broadcastKeySync()
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
	if s.Broker != nil {
		s.broadcastKeySync()
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

// SSE streaming endpoint: GET /api/v1/devices/{id}/events
func (s *Server) deviceEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", 500)
		return
	}
	if s.Broker == nil {
		http.Error(w, "broker disabled", 503)
		return
	}

	deviceID := r.PathValue("id")
	if _, err := s.Registry.Get(deviceID); err != nil {
		statusError(w, err)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ch, unsubscribe := s.Broker.Subscribe(deviceID)
	defer unsubscribe()

	// 1. Immediately send full snapshot to newly connected device
	payload, err := s.resolveDeviceKeySyncPayload(deviceID)
	if err == nil {
		dataBytes, _ := json.Marshal(payload)
		fmt.Fprintf(w, "event: key_sync\ndata: %s\n\n", dataBytes)
		flusher.Flush()
	}

	pingInterval := s.PingInterval
	if pingInterval <= 0 {
		pingInterval = 30 * time.Second
	}
	pingTicker := time.NewTicker(pingInterval)
	defer pingTicker.Stop()

	if s.Log != nil {
		s.Log.Info("sse_client_connected", "device_id", deviceID)
	}

	for {
		select {
		case <-r.Context().Done():
			if s.Log != nil {
				s.Log.Info("sse_client_disconnected", "device_id", deviceID)
			}
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if ev.Type != "" {
				fmt.Fprintf(w, "event: %s\n", ev.Type)
			}
			if ev.ID != "" {
				fmt.Fprintf(w, "id: %s\n", ev.ID)
			}
			fmt.Fprintf(w, "data: %s\n\n", ev.Data)
			flusher.Flush()
		case t := <-pingTicker.C:
			pingData, _ := json.Marshal(map[string]int64{"timestamp": t.Unix()})
			fmt.Fprintf(w, "event: ping\ndata: %s\n\n", pingData)
			flusher.Flush()
		}
	}
}

type AckRequest struct {
	Module         string `json:"module"`
	Status         string `json:"status"`
	AppliedVersion int64  `json:"applied_version"`
	AppliedHash    string `json:"applied_hash"`
	ErrorMessage   string `json:"error_message"`
}

// Client status ACK endpoint: POST /api/v1/devices/{id}/ack
func (s *Server) deviceAck(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	deviceID := r.PathValue("id")
	var req AckRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		http.Error(w, "invalid JSON request", 400)
		return
	}

	if err := s.Registry.UpdateSyncStatus(deviceID, req.Status, req.AppliedVersion, req.AppliedHash, req.ErrorMessage); err != nil {
		statusError(w, err)
		return
	}

	if s.Log != nil {
		s.Log.Info("device_ack_received",
			"device_id", deviceID,
			"module", req.Module,
			"status", req.Status,
			"applied_version", req.AppliedVersion,
			"applied_hash", req.AppliedHash,
			"error", req.ErrorMessage,
		)
	}

	// Self-healing reconcile: check if client hash matches expected server hash
	if req.Status == "synced" && req.AppliedHash != "" {
		expected, err := s.resolveDeviceKeySyncPayload(deviceID)
		if err == nil && req.AppliedHash != expected.Hash && s.Broker != nil {
			if s.Log != nil {
				s.Log.Warn("device_hash_mismatch_triggering_resync",
					"device_id", deviceID,
					"reported_hash", req.AppliedHash,
					"expected_hash", expected.Hash,
				)
			}
			dataBytes, _ := json.Marshal(expected)
			s.Broker.Publish(deviceID, broker.Event{
				Type: "key_sync",
				Data: string(dataBytes),
			})
		}
	}

	writeJSON(w, 200, map[string]any{"success": true})
}

// Pull keys fallback endpoint: GET /api/v1/devices/{id}/keys
func (s *Server) deviceKeys(w http.ResponseWriter, r *http.Request) {
	deviceID := r.PathValue("id")
	payload, err := s.resolveDeviceKeySyncPayload(deviceID)
	if err != nil {
		statusError(w, err)
		return
	}
	writeJSON(w, 200, payload)
}

func (s *Server) resolveDeviceKeySyncPayload(targetID string) (sshsync.KeySyncPayload, error) {
	_, err := s.Registry.Get(targetID)
	if err != nil {
		return sshsync.KeySyncPayload{}, err
	}

	policy, _ := acl.Load(s.ACLPath)
	all := s.Registry.List()
	allowed := policy.Resolve(targetID, all)

	var keys []sshsync.Key
	if s.AdminPublicKey != "" {
		keys = append(keys, sshsync.Key{DeviceID: "homeagent-admin", PublicKey: s.AdminPublicKey})
	}
	for _, d := range allowed {
		keys = append(keys, sshsync.Key{DeviceID: d.ID, PublicKey: d.PublicKey})
	}

	version := atomic.LoadInt64(&s.version)
	if version == 0 {
		version = 1
	}
	hash := sshsync.ComputeKeySetHash(keys)

	return sshsync.KeySyncPayload{
		Version: version,
		Hash:    hash,
		Keys:    keys,
	}, nil
}

func (s *Server) broadcastKeySync() {
	newVersion := atomic.AddInt64(&s.version, 1)
	active := s.Broker.ActiveDevices()
	for _, devID := range active {
		payload, err := s.resolveDeviceKeySyncPayload(devID)
		if err != nil {
			continue
		}
		payload.Version = newVersion
		dataBytes, err := json.Marshal(payload)
		if err != nil {
			continue
		}
		s.Broker.Publish(devID, broker.Event{
			Type: "key_sync",
			Data: string(dataBytes),
		})
	}
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
