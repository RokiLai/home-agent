package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"homeagent/internal/device"
)

// TestP1_CrossVersion_CandidateAgentToV062Server verifies the v0.6.2 response
// observed from the real handler built at Git commit 38c5e0c: the runtime schema
// already contains the v0.6.3-populated fields, so the first request succeeds.
// This HTTP substitute differs only by omitting v0.6.2 Registry persistence,
// which is covered by the corresponding real-handler protocol observation.
func TestP1_CrossVersion_CandidateAgentToV062Server(t *testing.T) {
	var attempts int
	var received deviceFactsPayload
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	facts := deviceFactsPayload{
		Hostname:     "windows-v063",
		AgentVersion: "v0.6.3",
		OS:           "windows",
		Arch:         "amd64",
		SSHUser:      "Administrator",
		SSHPort:      22,
		Runtime: &device.RuntimeFacts{
			ObservedAt:           time.Now().UTC(),
			UptimeSeconds:        285067,
			LogicalCPUCount:      8,
			MemoryTotalBytes:     33495388160,
			MemoryAvailableBytes: 17510576128,
			DiskMount:            "C:\\",
		},
	}

	_, downgraded, err := sendDeviceFactsWithStatus(context.Background(), server.Client(), []string{server.URL}, nil, "token", "dev-v063", facts)
	if err != nil {
		t.Fatalf("send facts to v0.6.2-compatible server: %v", err)
	}
	if downgraded || attempts != 1 {
		t.Fatalf("v0.6.2-compatible server must accept the first request: downgraded=%v attempts=%d", downgraded, attempts)
	}
	if received.Runtime == nil || received.Runtime.UptimeSeconds != facts.Runtime.UptimeSeconds || received.Runtime.MemoryTotalBytes != facts.Runtime.MemoryTotalBytes || received.Runtime.MemoryAvailableBytes != facts.Runtime.MemoryAvailableBytes {
		t.Fatalf("runtime fields changed across v0.6.2-compatible HTTP contract: got %+v want %+v", received.Runtime, facts.Runtime)
	}
}

// 候选 Agent -> 上一正式版本 Server：当旧 Server 拒绝未知 runtime 字段返回 400 时，候选 Agent 自动剥离 runtime 降级重试成功。
func TestP1_CrossVersion_CandidateAgentToLegacyServerFallback(t *testing.T) {
	var attempts int
	var receivedBodies [][]byte

	mockLegacyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		b, _ := io.ReadAll(r.Body)
		receivedBodies = append(receivedBodies, b)

		if bytes.Contains(b, []byte(`"runtime"`)) {
			// 旧 Server 严格校验未知字段并返回 400
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":"unknown field runtime"}`))
			return
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer mockLegacyServer.Close()

	// 候选 Agent 准备发送带 runtime 的 Facts
	facts := deviceFactsPayload{
		Hostname:     "candidate-node",
		AgentVersion: "v0.6.0",
		OS:           "linux",
		Arch:         "amd64",
		SSHUser:      "root",
		SSHPort:      22,
		Addresses:    []string{"192.168.1.101"},
		Runtime: &device.RuntimeFacts{
			ObservedAt:         time.Now().UTC(),
			DiskTotalBytes:     100 * 1024 * 1024 * 1024,
			DiskAvailableBytes: 50 * 1024 * 1024 * 1024,
		},
	}

	client := mockLegacyServer.Client()
	target, err := sendDeviceFacts(context.Background(), client, []string{mockLegacyServer.URL}, nil, "dev-tok", "dev-cand-1", facts)
	if err != nil {
		t.Fatalf("sendDeviceFacts failed during legacy fallback: %v", err)
	}
	if target != mockLegacyServer.URL {
		t.Fatalf("expected target %s, got %s", mockLegacyServer.URL, target)
	}
	if attempts != 2 {
		t.Fatalf("expected exactly 2 attempts (initial 400 + retry), got %d", attempts)
	}
	// 验证第二次请求已经剥离了 runtime
	if bytes.Contains(receivedBodies[1], []byte(`"runtime"`)) {
		t.Fatalf("second attempt must not contain runtime field: %s", string(receivedBodies[1]))
	}
}

// 候选 v0.6.4 Agent -> 上一正式版本 v0.6.3 Server：旧 Server 发生冲突时返回纯文本 409，候选 Agent 安全视为普通失败，不发生误追赶
func TestP1_CrossVersion_CandidateAgentToV063ServerPlain409(t *testing.T) {
	tempDir := t.TempDir()
	store := newRevisionStore(tempDir)
	var attempts int

	mockV063Server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte("revision conflict: received 1, current is 5"))
	}))
	defer mockV063Server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	cfg := networkReportConfig{
		ctx:          ctx,
		client:       mockV063Server.Client(),
		serverURLs:   []string{mockV063Server.URL},
		token:        "test-token",
		deviceID:     "dev-cand-v063",
		networkID:    "home",
		reportType:   reportTypeDeviceNetworkState,
		endpointPath: "/api/v1/devices/dev-cand-v063/network-state",
		buildPayload: func(rev uint64, observedAt time.Time) (any, error) {
			return map[string]any{"network_id": "home", "revision": rev, "observed_at": observedAt}, nil
		},
		store: store,
	}

	_ = sendNetworkReport(cfg)

	cur, err := store.Current(revisionKey{ReportType: reportTypeDeviceNetworkState, DeviceID: "dev-cand-v063", NetworkID: "home"})
	if err != nil || cur != 1 {
		t.Fatalf("revision must remain 1 when facing plain text 409: cur=%d err=%v", cur, err)
	}
}

// 候选 v0.6.4 Agent -> 候选 v0.6.4 Server：结构化 JSON 409 触发有界自动追赶成功
func TestP1_CrossVersion_CandidateAgentToV064ServerStructured409(t *testing.T) {
	tempDir := t.TempDir()
	store := newRevisionStore(tempDir)
	var receivedRevisions []uint64

	mockV064Server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		rev := uint64(req["revision"].(float64))
		receivedRevisions = append(receivedRevisions, rev)

		if len(receivedRevisions) == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error":             "revision_conflict",
				"current_revision":  42,
				"received_revision": rev,
			})
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"accepted_revision": rev,
			"changed":           false,
		})
	}))
	defer mockV064Server.Close()

	cfg := networkReportConfig{
		ctx:          context.Background(),
		client:       mockV064Server.Client(),
		serverURLs:   []string{mockV064Server.URL},
		token:        "test-token",
		deviceID:     "dev-cand-v064",
		networkID:    "home",
		reportType:   reportTypeDeviceNetworkState,
		endpointPath: "/api/v1/devices/dev-cand-v064/network-state",
		buildPayload: func(rev uint64, observedAt time.Time) (any, error) {
			return map[string]any{"network_id": "home", "revision": rev, "observed_at": observedAt}, nil
		},
		store: store,
	}

	if err := sendNetworkReport(cfg); err != nil {
		t.Fatalf("expected sendNetworkReport to recover and succeed: %v", err)
	}

	if len(receivedRevisions) != 2 || receivedRevisions[0] != 1 || receivedRevisions[1] != 43 {
		t.Fatalf("expected revisions [1, 43], got %v", receivedRevisions)
	}

	cur, err := store.Current(revisionKey{ReportType: reportTypeDeviceNetworkState, DeviceID: "dev-cand-v064", NetworkID: "home"})
	if err != nil || cur != 43 {
		t.Fatalf("expected persisted revision 43, got %d (err=%v)", cur, err)
	}
}
