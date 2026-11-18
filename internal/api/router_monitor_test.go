package api

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"homeagent/internal/auth"
	"homeagent/internal/device"
	"homeagent/internal/routermonitor"
)

func TestRouterMonitorRoutes(t *testing.T) {
	s, sm, _, registry, _ := setupTestServerWithAuth(t)
	s.RouterMonitor = routermonitor.NewLatest(filepath.Join(t.TempDir(), "monitor.json"))
	s.RouterMonitorAgent = "collector"
	s.RouterMonitorID = "router"
	h := s.Handler()
	for _, tc := range []struct {
		method, path, body string
		want               int
	}{
		{"GET", "/api/v1/router-monitor", "", 401},
		{"PUT", "/api/v1/devices/collector/router-monitor/router", `{"failed":true}`, 401},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)))
		if w.Code != tc.want {
			t.Fatalf("%s: %d, want %d", tc.path, w.Code, tc.want)
		}
	}
	token, _, err := sm.CreateSession("admin", "admin", false)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/api/v1/router-monitor", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"waiting"`) {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	const deviceToken = "test-dedicated-collector"
	for _, id := range []string{"collector", "other"} {
		_, err := registry.Save(device.Device{ID: id, Hostname: id, SSHUser: "agent", SSHPort: 22, PublicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIValKey12345678901234567890", DeviceTokenHash: auth.HashToken(deviceToken + "-" + id)})
		if err != nil {
			t.Fatal(err)
		}
	}

	for _, tc := range []struct {
		id, router, body string
		want             int
	}{
		{"other", "router", `{"failed":true}`, 403},
		{"collector", "other", `{"failed":true}`, 403},
		{"collector", "router", `{}`, 400},
		{"collector", "router", `{"raw":"invalid"}`, 400},
		{"collector", "router", `{"failed":true}`, 204},
		{"collector", "router", `{"raw":"pause_until_uptime=0 current_uptime=100\nwan\thealthy\tok\nchecked_uptime\t90\n"}`, 204},
	} {
		req := httptest.NewRequest("PUT", "/api/v1/devices/"+tc.id+"/router-monitor/"+tc.router, strings.NewReader(tc.body))
		req.Header.Set("Authorization", "Bearer "+deviceToken+"-"+tc.id)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != tc.want {
			t.Fatalf("%s: %d, want %d", tc.body, w.Code, tc.want)
		}
	}
}
