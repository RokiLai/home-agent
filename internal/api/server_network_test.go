package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"homeagent/internal/networkaddr"
	"homeagent/internal/servernetwork"
)

type mockProviderForAPI struct {
	addrs []networkaddr.ReportedIPv6Address
}

func (m *mockProviderForAPI) GetAddresses(ctx context.Context, iface string) ([]networkaddr.ReportedIPv6Address, error) {
	return m.addrs, nil
}

type mockPublisherForAPI struct {
	records     map[string][]netip.Addr
	upsertCalls int
}

func (m *mockPublisherForAPI) GetAAAA(ctx context.Context, record string) ([]netip.Addr, error) {
	return m.records[record], nil
}

func (m *mockPublisherForAPI) UpsertAAAA(ctx context.Context, record string, address netip.Addr, ttl int) error {
	m.upsertCalls++
	if m.records == nil {
		m.records = make(map[string][]netip.Addr)
	}
	m.records[record] = []netip.Addr{address}
	return nil
}

func (m *mockPublisherForAPI) DeleteAAAA(ctx context.Context, record string) error {
	delete(m.records, record)
	return nil
}

func TestServerNetworkAPI_GetAndPut(t *testing.T) {
	tempDir := t.TempDir()
	store := servernetwork.NewFileStateStore(tempDir)
	pub := &mockPublisherForAPI{records: make(map[string][]netip.Addr)}
	prov := &mockProviderForAPI{
		addrs: []networkaddr.ReportedIPv6Address{
			{Address: "240e:390:1::100", Interface: "en0"},
		},
	}
	collector := servernetwork.NewCollector("en0", prov)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	coord, err := servernetwork.NewCoordinator(servernetwork.Config{
		Enabled:   false,
		Interface: "en0",
		Records:   []string{"server.example.com"},
	}, collector, pub, store, logger)
	if err != nil {
		t.Fatalf("failed to create coordinator: %v", err)
	}

	server := &Server{
		Token:                    "admin-token",
		ServerNetworkCoordinator: coord,
		Log:                      logger,
	}
	handler := server.Handler()

	// 1. GET /api/v1/server/network - Initial status (disabled)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/server/network", nil)
	req.Header.Set("Authorization", "Bearer admin-token")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if resp["enabled"] != false || resp["status"] != "disabled" {
		t.Fatalf("unexpected response: %+v", resp)
	}

	// 2. 自动探测默认路由接口与稳定地址。
	candidateReq := httptest.NewRequest(http.MethodGet, "/api/v1/server/network/candidates", nil)
	candidateReq.Header.Set("Authorization", "Bearer admin-token")
	wCandidate := httptest.NewRecorder()
	handler.ServeHTTP(wCandidate, candidateReq)
	if wCandidate.Code != http.StatusOK {
		t.Fatalf("expected candidate status 200, got %d: %s", wCandidate.Code, wCandidate.Body.String())
	}
	var candidateResp map[string]any
	if err := json.Unmarshal(wCandidate.Body.Bytes(), &candidateResp); err != nil {
		t.Fatalf("unmarshal candidates: %v", err)
	}
	if candidateResp["resolved_interface"] != "en0" || candidateResp["resolved_address"] != "240e:390:1::100" {
		t.Fatalf("unexpected automatic detection: %+v", candidateResp)
	}

	// 3. 只读预检并获取单次令牌。
	validateBody, _ := json.Marshal(map[string]any{"detection_id": candidateResp["detection_id"], "records": []string{"hub.example.com"}})
	validateReq := httptest.NewRequest(http.MethodPost, "/api/v1/server/network/validate", bytes.NewReader(validateBody))
	validateReq.Header.Set("Authorization", "Bearer admin-token")
	wValidate := httptest.NewRecorder()
	handler.ServeHTTP(wValidate, validateReq)
	if wValidate.Code != http.StatusOK {
		t.Fatalf("expected validation status 200, got %d: %s", wValidate.Code, wValidate.Body.String())
	}
	var validateResp map[string]any
	if err := json.Unmarshal(wValidate.Body.Bytes(), &validateResp); err != nil {
		t.Fatalf("unmarshal validation: %v", err)
	}
	if validateResp["validation_status"] != "valid" {
		t.Fatalf("unexpected validation response: %+v", validateResp)
	}
	if pub.upsertCalls != 0 {
		t.Fatalf("read-only validation must not write DNS, got %d upserts", pub.upsertCalls)
	}

	// 4. PUT 不提交人工接口，使用预检令牌启用。
	body, _ := json.Marshal(map[string]any{
		"enabled":                   true,
		"records":                   []string{"hub.example.com"},
		"config_version":            1,
		"validation_token":          validateResp["validation_token"],
		"external_writer_confirmed": true,
	})
	putReq := httptest.NewRequest(http.MethodPut, "/api/v1/server/network", bytes.NewReader(body))
	putReq.Header.Set("Authorization", "Bearer admin-token")
	wPut := httptest.NewRecorder()
	handler.ServeHTTP(wPut, putReq)

	if wPut.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", wPut.Code, wPut.Body.String())
	}

	var putResp map[string]any
	if err := json.Unmarshal(wPut.Body.Bytes(), &putResp); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if putResp["enabled"] != true || putResp["status"] != "synced" || putResp["current_address"] != "240e:390:1::100" {
		t.Fatalf("unexpected put response: %+v", putResp)
	}

	// 5. 启用时不再要求接口，但没有预检令牌必须失败。
	invalidBody, _ := json.Marshal(map[string]any{
		"enabled": true,
		"records": []string{"hub.example.com"},
	})
	badReq := httptest.NewRequest(http.MethodPut, "/api/v1/server/network", bytes.NewReader(invalidBody))
	badReq.Header.Set("Authorization", "Bearer admin-token")
	wBad := httptest.NewRecorder()
	handler.ServeHTTP(wBad, badReq)
	if wBad.Code != http.StatusConflict {
		t.Fatalf("expected status 409 without validation token, got %d", wBad.Code)
	}

	// 6. Unauthorized check
	unauthReq := httptest.NewRequest(http.MethodGet, "/api/v1/server/network", nil)
	wUnauth := httptest.NewRecorder()
	handler.ServeHTTP(wUnauth, unauthReq)
	if wUnauth.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401 for missing token, got %d", wUnauth.Code)
	}
}
