package api

import (
	"bytes"
	"encoding/json"
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

func TestRouterMonitorEventsRoutes(t *testing.T) {
	s, sm, _, registry, _ := setupTestServerWithAuth(t)
	dir := t.TempDir()
	s.RouterMonitor = routermonitor.NewLatest(filepath.Join(dir, "monitor.json"))
	s.RouterEvents = routermonitor.NewEventStore(dir, "collector", "router")
	s.RouterMonitorAgent = "collector"
	s.RouterMonitorID = "router"
	h := s.Handler()

	// 1. 未授权访问拒绝
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/router-monitor/events", nil))
	if w.Code != 401 {
		t.Fatalf("expected 401, got %d", w.Code)
	}

	const deviceToken = "test-token"
	_, err := registry.Save(device.Device{
		ID:              "collector",
		Hostname:        "collector",
		SSHUser:         "agent",
		SSHPort:         22,
		PublicKey:       "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIValKey12345678901234567890",
		DeviceTokenHash: auth.HashToken(deviceToken + "-collector"),
	})
	if err != nil {
		t.Fatal(err)
	}

	// 2. 上报事件批次
	eventBody := `{
		"boot_id": "b1",
		"generation": "g1",
		"earliest_seq": 1,
		"latest_seq": 1,
		"events": [
			{"version":1,"boot_id":"b1","generation":"g1","seq":1,"cycle":1,"component":"wan","event_type":"observed","reason":"ok","uptime":10}
		]
	}`
	req := httptest.NewRequest("POST", "/api/v1/devices/collector/router-monitor/router/events", strings.NewReader(eventBody))
	req.Header.Set("Authorization", "Bearer "+deviceToken+"-collector")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("post events status: %d, body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"ack_cursor":1`) {
		t.Fatalf("expected ack_cursor 1, got %s", w.Body.String())
	}

	// 3. Admin 授权查询
	adminToken, _, err := sm.CreateSession("admin", "admin", false)
	if err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest("GET", "/api/v1/router-monitor/events", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: adminToken})
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("get events status: %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"items"`) || !strings.Contains(w.Body.String(), `"observed"`) {
		t.Fatalf("events body unexpected: %s", w.Body.String())
	}
}

func TestRouterMonitorE2ERealPortRace(t *testing.T) {
	s, sm, _, registry, _ := setupTestServerWithAuth(t)
	dir := t.TempDir()
	s.RouterMonitor = routermonitor.NewLatest(filepath.Join(dir, "monitor.json"))
	s.RouterEvents = routermonitor.NewEventStore(dir, "collector", "router")
	s.RouterMonitorAgent = "collector"
	s.RouterMonitorID = "router"

	const deviceToken = "test-token"
	_, err := registry.Save(device.Device{
		ID:              "collector",
		Hostname:        "collector",
		SSHUser:         "agent",
		SSHPort:         22,
		PublicKey:       "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIValKey12345678901234567890",
		DeviceTokenHash: auth.HashToken(deviceToken + "-collector"),
	})
	if err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	// 1. Agent post events
	eventBatch := routermonitor.EventBatch{
		BootID:      "b1",
		Generation:  "g1",
		EarliestSeq: 1,
		LatestSeq:   2,
		Events: []routermonitor.Event{
			{Version: 1, BootID: "b1", Generation: "g1", Seq: 1, Cycle: 1, Component: "wan", EventType: "action_requested", ActionID: "act-1", Reason: "wan_down", Uptime: 100},
			{Version: 1, BootID: "b1", Generation: "g1", Seq: 2, Cycle: 2, Component: "wan", EventType: "recovered", ActionID: "act-1", Reason: "ok", Uptime: 160},
		},
	}
	body, _ := json.Marshal(eventBatch)
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/devices/collector/router-monitor/router/events", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+deviceToken+"-collector")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", res.StatusCode)
	}
	res.Body.Close()

	// 2. Query events via Admin session
	adminToken, _, _ := sm.CreateSession("admin", "admin", false)
	getReq, _ := http.NewRequest("GET", ts.URL+"/api/v1/router-monitor/events", nil)
	getReq.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: adminToken})
	getRes, err := http.DefaultClient.Do(getReq)
	if err != nil {
		t.Fatal(err)
	}
	if getRes.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", getRes.StatusCode)
	}
	defer getRes.Body.Close()
	var queryResp struct {
		Items []routermonitor.TimelineItem `json:"items"`
	}
	if err := json.NewDecoder(getRes.Body).Decode(&queryResp); err != nil {
		t.Fatal(err)
	}
	if len(queryResp.Items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(queryResp.Items))
	}
	if queryResp.Items[1].DisplayStatus != "已恢复" {
		t.Fatalf("expected 已恢复, got %s", queryResp.Items[1].DisplayStatus)
	}
}

func TestRouterMonitorErrorBranches(t *testing.T) {
	s, sm, _, registry, _ := setupTestServerWithAuth(t)
	dir := t.TempDir()
	s.RouterEvents = routermonitor.NewEventStore(dir, "collector", "router")
	s.RouterMonitorAgent = "collector"
	s.RouterMonitorID = "router"
	h := s.Handler()

	const deviceToken = "test-token"
	_, _ = registry.Save(device.Device{
		ID:              "collector",
		Hostname:        "collector",
		SSHUser:         "agent",
		SSHPort:         22,
		PublicKey:       "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIValKey12345678901234567890",
		DeviceTokenHash: auth.HashToken(deviceToken + "-collector"),
	})

	// 1. 无效 JSON 批次
	req := httptest.NewRequest("POST", "/api/v1/devices/collector/router-monitor/router/events", strings.NewReader("bad json"))
	req.Header.Set("Authorization", "Bearer "+deviceToken+"-collector")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}

	// 2. 存在未声明 Gap 触发 409 Conflict
	// 先提交第 1 条事件建立游标
	initBatch := `{"generation":"g1","boot_id":"b1","earliest_seq":1,"latest_seq":1,"events":[{"version":1,"seq":1,"event_type":"observed"}]}`
	req = httptest.NewRequest("POST", "/api/v1/devices/collector/router-monitor/router/events", strings.NewReader(initBatch))
	req.Header.Set("Authorization", "Bearer "+deviceToken+"-collector")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	// 游标已推进到 1，若 EarliestSeq=1 但 events 首条 seq 为 5，则会触发 ErrGapDetected
	badUndeclared := `{"generation":"g1","boot_id":"b1","earliest_seq":1,"latest_seq":5,"events":[{"version":1,"seq":5,"event_type":"observed"}]}`
	req = httptest.NewRequest("POST", "/api/v1/devices/collector/router-monitor/router/events", strings.NewReader(badUndeclared))
	req.Header.Set("Authorization", "Bearer "+deviceToken+"-collector")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 conflict, got %d", w.Code)
	}

	// 3. 非管理员访问事件查询被拒绝
	ownerUser, _ := sm.CreateUser("owner_test", "Owner123!", auth.RoleOwner, "")
	viewerUser, _ := sm.CreateUser("guest_viewer", "Viewer123!", auth.RoleViewer, ownerUser.ID)
	viewerToken, _, err := sm.CreateUserSession(viewerUser.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest("GET", "/api/v1/router-monitor/events", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: viewerToken})
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", w.Code)
	}
}
