package api

import (
	"encoding/json"
	"errors"
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

func (s *Server) readRouterMonitorEvents(w http.ResponseWriter, r *http.Request) {
	actor := auth.GetActorFromContext(r.Context())
	if actor == nil || (actor.Role != auth.RoleOwner && actor.Role != auth.RoleAdmin) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var events []routermonitor.Event
	if s.RouterEvents != nil {
		var err error
		events, err = s.RouterEvents.QueryEvents(100)
		if err != nil {
			http.Error(w, "events unavailable", http.StatusServiceUnavailable)
			return
		}
	}
	items := routermonitor.BuildTimelineItems(events)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"items": items,
	})
}

func (s *Server) postRouterMonitorEvents(w http.ResponseWriter, r *http.Request) {
	if s.RouterEvents == nil || r.PathValue("id") != s.RouterMonitorAgent || r.PathValue("router") != s.RouterMonitorID {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	device, err := s.Registry.Get(r.PathValue("id"))
	if err != nil || device.DeviceTokenHash == "" {
		http.Error(w, "dedicated device credential required", http.StatusForbidden)
		return
	}

	received := time.Now()
	var batch routermonitor.EventBatch
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536))
	dec.DisallowUnknownFields()
	if dec.Decode(&batch) != nil || dec.Decode(new(any)) != io.EOF || batch.Generation == "" {
		http.Error(w, "invalid batch", http.StatusBadRequest)
		return
	}

	ackCursor, err := s.RouterEvents.SaveEvents(batch, received)
	if err != nil {
		if errors.Is(err, routermonitor.ErrGapDetected) {
			http.Error(w, "gap detected", http.StatusConflict)
			return
		}
		http.Error(w, "could not save events", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ack_cursor": ackCursor,
	})
}
