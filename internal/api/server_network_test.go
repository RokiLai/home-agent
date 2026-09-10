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
	records map[string][]netip.Addr
}

func (m *mockPublisherForAPI) GetAAAA(ctx context.Context, record string) ([]netip.Addr, error) {
	return m.records[record], nil
}

func (m *mockPublisherForAPI) UpsertAAAA(ctx context.Context, record string, address netip.Addr, ttl int) error {
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

	// 2. PUT /api/v1/server/network - Enable and update records
	body, _ := json.Marshal(map[string]any{
		"enabled":   true,
		"interface": "en0",
		"records":   []string{"hub.example.com"},
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

	// 3. Validation: Enabled=true with empty interface should fail
	invalidBody, _ := json.Marshal(map[string]any{
		"enabled":   true,
		"interface": "",
		"records":   []string{"hub.example.com"},
	})
	badReq := httptest.NewRequest(http.MethodPut, "/api/v1/server/network", bytes.NewReader(invalidBody))
	badReq.Header.Set("Authorization", "Bearer admin-token")
	wBad := httptest.NewRecorder()
	handler.ServeHTTP(wBad, badReq)
	if wBad.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400 for empty interface, got %d", wBad.Code)
	}

	// 4. Unauthorized check
	unauthReq := httptest.NewRequest(http.MethodGet, "/api/v1/server/network", nil)
	wUnauth := httptest.NewRecorder()
	handler.ServeHTTP(wUnauth, unauthReq)
	if wUnauth.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401 for missing token, got %d", wUnauth.Code)
	}
}
