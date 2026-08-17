package ddns

import (
	"context"
	"net/netip"
	"sync"
	"testing"
	"time"

	"homeagent/internal/devicestate"
	"homeagent/internal/networkaddr"
	"homeagent/internal/prefixstate"
)

type mockPublisher struct {
	mu      sync.Mutex
	records map[string]netip.Addr
	upserts int
	deletes int
}

func newMockPublisher() *mockPublisher {
	return &mockPublisher{
		records: make(map[string]netip.Addr),
	}
}

func (m *mockPublisher) GetAAAA(ctx context.Context, record string) ([]netip.Addr, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ip, ok := m.records[record]
	if !ok {
		return nil, nil
	}
	return []netip.Addr{ip}, nil
}

func (m *mockPublisher) UpsertAAAA(ctx context.Context, record string, address netip.Addr, ttl int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records[record] = address
	m.upserts++
	return nil
}

func (m *mockPublisher) DeleteAAAA(ctx context.Context, record string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.records, record)
	m.deletes++
	return nil
}

func TestDDNSService_LifecycleAndTransitions(t *testing.T) {
	devSvc := devicestate.NewService(nil)
	prefSvc := prefixstate.NewService(nil)
	pub := newMockPublisher()

	cfg := Config{
		Enabled:                 true,
		EmptyAddressGracePeriod: 100 * time.Millisecond,
		SyncTimeout:             2 * time.Second,
		TTL:                     120,
		Networks: map[string]NetworkConfig{
			"home": {
				RouterDeviceID: "router-1",
				PrefixStateTTL: 15 * time.Minute,
			},
		},
		Devices: map[string]DeviceConfig{
			"macbook-1": {
				NetworkID: "home",
				Record:    "macbook.example.com",
			},
		},
	}

	ddnsSvc := NewService(cfg, devSvc, prefSvc, pub, nil)
	ctx := context.Background()
	t0 := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)

	// 1. Router reports prefix A (240e:10::/64)
	prefA := []prefixstate.ReportedIPv6Prefix{
		{Prefix: "240e:10::/64"},
	}
	_, _, err := prefSvc.UpdateRouterPrefixes("router-1", "home", 1, t0, prefA)
	if err != nil {
		t.Fatalf("failed to update router prefix: %v", err)
	}

	// 2. MacBook reports candidate A (240e:10::1)
	candA := []networkaddr.ReportedIPv6Address{
		{Address: "240e:10::1", Interface: "en0"},
	}
	_, _, err = devSvc.UpdateReportedAddresses("macbook-1", "home", 1, t0, candA)
	if err != nil {
		t.Fatalf("failed to update device state: %v", err)
	}

	// 3. Reconcile macbook-1 -> should publish AAAA 240e:10::1
	if err := ddnsSvc.ReconcileDevice(ctx, "macbook-1"); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	st, _ := devSvc.Get("macbook-1")
	if st.SyncStatus != devicestate.SyncStatusSynced || st.AppliedAddress != "240e:10::1" {
		t.Fatalf("expected synced with 240e:10::1, got status=%s, applied=%s", st.SyncStatus, st.AppliedAddress)
	}
	if pub.records["macbook.example.com"].String() != "240e:10::1" {
		t.Fatalf("mock publisher missing record: %v", pub.records)
	}

	// 4. Idempotent check -> no duplicate upsert
	upsertCountBefore := pub.upserts
	if err := ddnsSvc.ReconcileDevice(ctx, "macbook-1"); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	if pub.upserts != upsertCountBefore {
		t.Errorf("expected no additional upsert calls for unchanged state")
	}

	// 5. Dual prefix transition A -> A+B -> B
	// Router drops A and now only has B (240e:20::/64)
	prefB := []prefixstate.ReportedIPv6Prefix{
		{Prefix: "240e:20::/64"},
	}
	_, _, err = prefSvc.UpdateRouterPrefixes("router-1", "home", 2, t0.Add(time.Minute), prefB)
	if err != nil {
		t.Fatalf("failed to update router prefix B: %v", err)
	}

	// Device reports A and B (240e:10::1 and 240e:20::2)
	candAB := []networkaddr.ReportedIPv6Address{
		{Address: "240e:10::1", Interface: "en0"},
		{Address: "240e:20::2", Interface: "en0"},
	}
	_, _, err = devSvc.UpdateReportedAddresses("macbook-1", "home", 2, t0.Add(time.Minute), candAB)
	if err != nil {
		t.Fatalf("failed to update device state B: %v", err)
	}

	// Reconcile -> should switch to B because A is no longer in router's active prefixes!
	if err := ddnsSvc.ReconcileNetwork(ctx, "home"); err != nil {
		t.Fatalf("reconcile network failed: %v", err)
	}

	st, _ = devSvc.Get("macbook-1")
	if st.SyncStatus != devicestate.SyncStatusSynced || st.AppliedAddress != "240e:20::2" {
		t.Fatalf("expected synced with 240e:20::2, got status=%s, applied=%s", st.SyncStatus, st.AppliedAddress)
	}
	if pub.records["macbook.example.com"].String() != "240e:20::2" {
		t.Fatalf("expected 240e:20::2 in publisher, got %v", pub.records)
	}

	// 6. Address disappears -> device reports empty candidates
	_, _, err = devSvc.UpdateReportedAddresses("macbook-1", "home", 3, t0.Add(2*time.Minute), nil)
	if err != nil {
		t.Fatalf("failed to update empty address: %v", err)
	}

	// Reconcile -> should enter grace_period and NOT delete record yet!
	if err := ddnsSvc.ReconcileDevice(ctx, "macbook-1"); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	st, _ = devSvc.Get("macbook-1")
	if st.SyncStatus != devicestate.SyncStatusGracePeriod {
		t.Fatalf("expected grace_period status, got %s", st.SyncStatus)
	}
	if pub.records["macbook.example.com"].String() != "240e:20::2" {
		t.Errorf("record should still be preserved during grace period")
	}

	// 7. Wait until grace period expires -> Reconcile -> deletes record!
	time.Sleep(120 * time.Millisecond)
	if err := ddnsSvc.ReconcileDevice(ctx, "macbook-1"); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	st, _ = devSvc.Get("macbook-1")
	if st.SyncStatus != devicestate.SyncStatusSynced || st.AppliedAddress != "" {
		t.Fatalf("expected synced and empty applied address after deletion, got status=%s, applied=%s", st.SyncStatus, st.AppliedAddress)
	}
	if len(pub.records) != 0 {
		t.Errorf("expected AAAA record to be deleted, got %v", pub.records)
	}
}
