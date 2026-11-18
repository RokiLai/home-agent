package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"homeagent/internal/auth"
	"homeagent/internal/routermonitor"
)

func (s *Server) readRouterMonitor(w http.ResponseWriter, r *http.Request) {
	actor := auth.GetActorFromContext(r.Context())
	if actor == nil || (actor.Role != auth.RoleOwner && actor.Role != auth.RoleAdmin) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	v := routermonitor.View{State: "unconfigured"}
	if s.RouterMonitor != nil {
		var err error
		v, err = s.RouterMonitor.Read(time.Now())
		if err != nil {
			http.Error(w, "monitor data unavailable", http.StatusServiceUnavailable)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) putRouterMonitor(w http.ResponseWriter, r *http.Request) {
	if s.RouterMonitor == nil || r.PathValue("id") != s.RouterMonitorAgent || r.PathValue("router") != s.RouterMonitorID {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	device, err := s.Registry.Get(r.PathValue("id"))
	if err != nil || device.DeviceTokenHash == "" {
		http.Error(w, "dedicated device credential required", http.StatusForbidden)
		return
	}

	received := time.Now()
	var report routermonitor.Report
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536))
	dec.DisallowUnknownFields()
	if dec.Decode(&report) != nil || dec.Decode(new(any)) != io.EOF || (report.Raw != "") == report.Failed {
		http.Error(w, "invalid report", http.StatusBadRequest)
		return
	}
	if !report.Failed {
		if _, err := routermonitor.ParseSnapshot(strings.NewReader(report.Raw)); err != nil {
			http.Error(w, "invalid snapshot", http.StatusBadRequest)
			return
		}
	}
	if err := s.RouterMonitor.Save(report, received); err != nil {
		http.Error(w, "could not save snapshot", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
