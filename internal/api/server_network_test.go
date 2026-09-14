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
	"time"

	"homeagent/internal/auth"
	"homeagent/internal/devicestate"
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

func TestServerIPv6TextEndpoint(t *testing.T) {
	prov := &mockProviderForAPI{
		addrs: []networkaddr.ReportedIPv6Address{
			{Address: "240e:390:1::100", Interface: "en0"},
		},
	}
	collector := servernetwork.NewCollector("en0", prov)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	server := &Server{
		Token:               "admin-token",
		ServerIPv6Collector: collector,
		Log:                 logger,
	}
	handler := server.Handler()

	// 1. 本地回环 IPv4 (127.0.0.1) 免密直通
	reqLoop4 := httptest.NewRequest(http.MethodGet, "/api/v1/server/ipv6", nil)
	reqLoop4.RemoteAddr = "127.0.0.1:54321"
	wLoop4 := httptest.NewRecorder()
	handler.ServeHTTP(wLoop4, reqLoop4)
	if wLoop4.Code != http.StatusOK {
		t.Fatalf("expected 200 for 127.0.0.1 loopback, got %d: %s", wLoop4.Code, wLoop4.Body.String())
	}
	if ct := wLoop4.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Fatalf("expected text/plain; charset=utf-8, got %q", ct)
	}
	if body := wLoop4.Body.String(); body != "240e:390:1::100\n" {
		t.Fatalf("expected 240e:390:1::100\\n, got %q", body)
	}

	// 2. 本地回环 IPv6 (::1) 免密直通
	reqLoop6 := httptest.NewRequest(http.MethodGet, "/api/v1/server/ipv6", nil)
	reqLoop6.RemoteAddr = "[::1]:54321"
	wLoop6 := httptest.NewRecorder()
	handler.ServeHTTP(wLoop6, reqLoop6)
	if wLoop6.Code != http.StatusOK {
		t.Fatalf("expected 200 for [::1] loopback, got %d: %s", wLoop6.Code, wLoop6.Body.String())
	}
	if body := wLoop6.Body.String(); body != "240e:390:1::100\n" {
		t.Fatalf("expected 240e:390:1::100\\n, got %q", body)
	}

	// 3. 外部网络 (192.168.1.50) 无 Token 被拦截
	reqExtUnauth := httptest.NewRequest(http.MethodGet, "/api/v1/server/ipv6", nil)
	reqExtUnauth.RemoteAddr = "192.168.1.50:54321"
	wExtUnauth := httptest.NewRecorder()
	handler.ServeHTTP(wExtUnauth, reqExtUnauth)
	if wExtUnauth.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for external unauthenticated request, got %d", wExtUnauth.Code)
	}

	// 4. 外部网络带 Authorization Header 通过
	reqExtAuthHeader := httptest.NewRequest(http.MethodGet, "/api/v1/server/ipv6", nil)
	reqExtAuthHeader.RemoteAddr = "192.168.1.50:54321"
	reqExtAuthHeader.Header.Set("Authorization", "Bearer admin-token")
	wExtAuthHeader := httptest.NewRecorder()
	handler.ServeHTTP(wExtAuthHeader, reqExtAuthHeader)
	if wExtAuthHeader.Code != http.StatusOK {
		t.Fatalf("expected 200 for external authenticated header request, got %d: %s", wExtAuthHeader.Code, wExtAuthHeader.Body.String())
	}

	// 5. 外部网络带 Query 参数 ?token=admin-token 通过
	reqExtAuthQuery := httptest.NewRequest(http.MethodGet, "/api/v1/server/ipv6?token=admin-token", nil)
	reqExtAuthQuery.RemoteAddr = "192.168.1.50:54321"
	wExtAuthQuery := httptest.NewRecorder()
	handler.ServeHTTP(wExtAuthQuery, reqExtAuthQuery)
	if wExtAuthQuery.Code != http.StatusOK {
		t.Fatalf("expected 200 for external authenticated query request, got %d: %s", wExtAuthQuery.Code, wExtAuthQuery.Body.String())
	}

	sm, err := auth.NewSessionManager(t.TempDir() + "/sessions.json")
	if err != nil {
		t.Fatalf("failed to create session manager: %v", err)
	}
	serverWithSM := &Server{
		Token:               "admin-token",
		ServerIPv6Collector: collector,
		SessionManager:      sm,
		Log:                 logger,
	}
	handlerWithSM := serverWithSM.Handler()
	wExtAuthWithSM := httptest.NewRecorder()
	handlerWithSM.ServeHTTP(wExtAuthWithSM, reqExtAuthQuery)
	if wExtAuthWithSM.Code != http.StatusOK {
		t.Fatalf("expected 200 for external token request when SessionManager is enabled, got %d: %s", wExtAuthWithSM.Code, wExtAuthWithSM.Body.String())
	}

	// 6. 无有效地址时返回 503 纯文本
	prov.addrs = nil
	reqNoAddr := httptest.NewRequest(http.MethodGet, "/api/v1/server/ipv6", nil)
	reqNoAddr.RemoteAddr = "127.0.0.1:54321"
	wNoAddr := httptest.NewRecorder()
	handler.ServeHTTP(wNoAddr, reqNoAddr)
	if wNoAddr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when no valid address, got %d: %s", wNoAddr.Code, wNoAddr.Body.String())
	}
	if ct := wNoAddr.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Fatalf("expected text/plain on failure, got %q", ct)
	}
}

func TestDeviceIPv6TextEndpoint_LoopbackAndToken(t *testing.T) {
	devStateSvc := devicestate.NewService(nil)
	_, _, err := devStateSvc.UpdateReportedAddresses(
		"dev-1", "home", 1, time.Now().UTC(),
		[]networkaddr.ReportedIPv6Address{{Address: "2001:db8:1::100"}},
	)
	if err != nil {
		t.Fatalf("failed to update addresses: %v", err)
	}

	server := &Server{
		Token:              "admin-token",
		DeviceStateService: devStateSvc,
		Log:                slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	handler := server.Handler()

	// 1. 本地回环请求免密访问设备 IPv6
	reqLoop := httptest.NewRequest(http.MethodGet, "/api/v1/devices/dev-1/ipv6", nil)
	reqLoop.RemoteAddr = "127.0.0.1:43210"
	wLoop := httptest.NewRecorder()
	handler.ServeHTTP(wLoop, reqLoop)
	if wLoop.Code != http.StatusOK {
		t.Fatalf("expected 200 for loopback device ipv6 query, got %d: %s", wLoop.Code, wLoop.Body.String())
	}
	if body := wLoop.Body.String(); body != "2001:db8:1::100\n" {
		t.Fatalf("expected 2001:db8:1::100\\n, got %q", body)
	}

	// 2. 外部请求无 Token 返回 401
	reqExtUnauth := httptest.NewRequest(http.MethodGet, "/api/v1/devices/dev-1/ipv6", nil)
	reqExtUnauth.RemoteAddr = "192.168.1.50:43210"
	wExtUnauth := httptest.NewRecorder()
	handler.ServeHTTP(wExtUnauth, reqExtUnauth)
	if wExtUnauth.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unauthenticated external request, got %d", wExtUnauth.Code)
	}

	// 3. 外部请求携带 Query 参数 ?token=admin-token 返回 200
	reqExtToken := httptest.NewRequest(http.MethodGet, "/api/v1/devices/dev-1/ipv6?token=admin-token", nil)
	reqExtToken.RemoteAddr = "192.168.1.50:43210"
	wExtToken := httptest.NewRecorder()
	handler.ServeHTTP(wExtToken, reqExtToken)
	if wExtToken.Code != http.StatusOK {
		t.Fatalf("expected 200 for query token external request, got %d: %s", wExtToken.Code, wExtToken.Body.String())
	}
	if body := wExtToken.Body.String(); body != "2001:db8:1::100\n" {
		t.Fatalf("expected 2001:db8:1::100\\n, got %q", body)
	}
}

func TestServerNetworkCandidates_WithoutCoordinator(t *testing.T) {
	prov := &mockProviderForAPI{
		addrs: []networkaddr.ReportedIPv6Address{
			{Address: "240e:390:1::100", Interface: "en0"},
		},
	}
	collector := servernetwork.NewCollector("en0", prov)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	server := &Server{
		Token:               "admin-token",
		ServerIPv6Collector: collector,
		Log:                 logger,
	}
	handler := server.Handler()

	// 1. 无 Coordinator 时 GET /api/v1/server/network/candidates 探测成功
	req := httptest.NewRequest(http.MethodGet, "/api/v1/server/network/candidates", nil)
	req.Header.Set("Authorization", "Bearer admin-token")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if resp["status"] != "ready" {
		t.Fatalf("expected status ready, got %v", resp["status"])
	}
	if resp["resolved_interface"] != "en0" || resp["resolved_address"] != "240e:390:1::100" {
		t.Fatalf("unexpected detection: %+v", resp)
	}

	// 2. 无有效地址时返回 200 与 no_address 状态，不得返回 501
	emptyProv := &mockProviderForAPI{addrs: nil}
	emptyCollector := servernetwork.NewCollector("en0", emptyProv)
	serverEmpty := &Server{
		Token:               "admin-token",
		ServerIPv6Collector: emptyCollector,
		Log:                 logger,
	}
	emptyHandler := serverEmpty.Handler()

	reqEmpty := httptest.NewRequest(http.MethodGet, "/api/v1/server/network/candidates", nil)
	reqEmpty.Header.Set("Authorization", "Bearer admin-token")
	wEmpty := httptest.NewRecorder()
	emptyHandler.ServeHTTP(wEmpty, reqEmpty)

	if wEmpty.Code != http.StatusOK {
		t.Fatalf("expected 200 for no_address, got %d: %s", wEmpty.Code, wEmpty.Body.String())
	}
	var emptyResp map[string]any
	if err := json.Unmarshal(wEmpty.Body.Bytes(), &emptyResp); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if emptyResp["status"] != "no_address" {
		t.Fatalf("expected status no_address, got %v", emptyResp["status"])
	}

	// 3. GET /api/v1/server/network 在无 Coordinator 时返回可用状态
	reqNet := httptest.NewRequest(http.MethodGet, "/api/v1/server/network", nil)
	reqNet.Header.Set("Authorization", "Bearer admin-token")
	wNet := httptest.NewRecorder()
	handler.ServeHTTP(wNet, reqNet)

	if wNet.Code != http.StatusOK {
		t.Fatalf("expected 200 for network status, got %d: %s", wNet.Code, wNet.Body.String())
	}
	var netResp map[string]any
	if err := json.Unmarshal(wNet.Body.Bytes(), &netResp); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if netResp["status"] != "standalone_detector" {
		t.Fatalf("expected status standalone_detector, got %v", netResp["status"])
	}
	if netResp["resolved_interface"] != "en0" || netResp["resolved_address"] != "240e:390:1::100" {
		t.Fatalf("unexpected netResp: %+v", netResp)
	}
}
