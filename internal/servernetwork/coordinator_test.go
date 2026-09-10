package servernetwork

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/netip"
	"sync"
	"testing"
	"time"

	"homeagent/internal/networkaddr"
)

type mockDNSPublisher struct {
	mu          sync.Mutex
	records     map[string][]netip.Addr
	upsertErr   error
	getErr      error
	upsertCalls int
	getCalls    int
}

func newMockDNSPublisher() *mockDNSPublisher {
	return &mockDNSPublisher{
		records: make(map[string][]netip.Addr),
	}
}

func (m *mockDNSPublisher) GetAAAA(ctx context.Context, record string) ([]netip.Addr, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.getCalls++
	if m.getErr != nil {
		return nil, m.getErr
	}
	return m.records[record], nil
}

func (m *mockDNSPublisher) UpsertAAAA(ctx context.Context, record string, address netip.Addr, ttl int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.upsertCalls++
	if m.upsertErr != nil {
		return m.upsertErr
	}
	m.records[record] = []netip.Addr{address}
	return nil
}

func (m *mockDNSPublisher) DeleteAAAA(ctx context.Context, record string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.records, record)
	return nil
}

func TestCoordinator_ReconcileSuccessAndVerification(t *testing.T) {
	tempDir := t.TempDir()
	store := NewFileStateStore(tempDir)
	pub := newMockDNSPublisher()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	prov := &mockAddressProvider{
		addrs: []networkaddr.ReportedIPv6Address{
			{Address: "240e:390:1234::100", Interface: "en0"},
		},
	}
	collector := NewCollector("en0", prov)

	coord, err := NewCoordinator(Config{
		Enabled:   true,
		Interface: "en0",
		Records:   []string{"hub.example.com"},
		Interval:  100 * time.Millisecond,
		Debounce:  10 * time.Millisecond,
		TTL:       300,
	}, collector, pub, store, logger)
	if err != nil {
		t.Fatalf("failed to create coordinator: %v", err)
	}

	ctx := context.Background()
	if err := coord.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	state, err := store.Load()
	if err != nil {
		t.Fatalf("load state failed: %v", err)
	}

	rec := state.Records["hub.example.com"]
	if rec == nil {
		t.Fatalf("record state not found")
	}
	if rec.Status != StatusSynced {
		t.Errorf("expected status %s, got %s", StatusSynced, rec.Status)
	}
	if rec.ConfirmedAddress != "240e:390:1234::100" {
		t.Errorf("expected confirmed address 240e:390:1234::100, got %s", rec.ConfirmedAddress)
	}
	if rec.LastError != "" {
		t.Errorf("expected empty last error, got %s", rec.LastError)
	}
}

func TestCoordinator_ReconcileAddressLossPreservesRecord(t *testing.T) {
	tempDir := t.TempDir()
	store := NewFileStateStore(tempDir)
	pub := newMockDNSPublisher()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// Pre-populate synced state
	_ = store.Save(&PersistedState{
		Records: map[string]*RecordState{
			"hub.example.com": {
				Record:           "hub.example.com",
				ConfirmedAddress: "240e:390:1234::100",
				ConfirmedVersion: 1,
				Status:           StatusSynced,
			},
		},
	})
	_ = pub.UpsertAAAA(context.Background(), "hub.example.com", netip.MustParseAddr("240e:390:1234::100"), 300)

	// Now address disappears
	prov := &mockAddressProvider{addrs: []networkaddr.ReportedIPv6Address{}}
	collector := NewCollector("en0", prov)

	coord, err := NewCoordinator(Config{
		Enabled:   true,
		Interface: "en0",
		Records:   []string{"hub.example.com"},
	}, collector, pub, store, logger)
	if err != nil {
		t.Fatalf("failed to create coordinator: %v", err)
	}

	if err := coord.Reconcile(context.Background()); err != nil {
		t.Fatalf("unexpected reconcile error: %v", err)
	}

	state, _ := store.Load()
	rec := state.Records["hub.example.com"]
	if rec.Status != StatusWaitingAddress {
		t.Errorf("expected status %s, got %s", StatusWaitingAddress, rec.Status)
	}
	// DNS record must NOT be deleted
	records, _ := pub.GetAAAA(context.Background(), "hub.example.com")
	if len(records) != 1 || records[0].String() != "240e:390:1234::100" {
		t.Errorf("expected preserved DNS record 240e:390:1234::100, got %v", records)
	}
}

func TestCoordinator_DetectExternalRewriteAndReconcile(t *testing.T) {
	tempDir := t.TempDir()
	store := NewFileStateStore(tempDir)
	pub := newMockDNSPublisher()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	prov := &mockAddressProvider{
		addrs: []networkaddr.ReportedIPv6Address{
			{Address: "240e:390:1234::100", Interface: "en0"},
		},
	}
	collector := NewCollector("en0", prov)

	coord, _ := NewCoordinator(Config{
		Enabled:   true,
		Interface: "en0",
		Records:   []string{"hub.example.com"},
	}, collector, pub, store, logger)

	_ = coord.Reconcile(context.Background())

	// Simulate external overwrite in DNS
	_ = pub.UpsertAAAA(context.Background(), "hub.example.com", netip.MustParseAddr("2001:db8::999"), 300)

	// Next reconcile detects mismatch and re-upserts desired address
	if err := coord.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	records, _ := pub.GetAAAA(context.Background(), "hub.example.com")
	if len(records) != 1 || records[0].String() != "240e:390:1234::100" {
		t.Errorf("expected recovered DNS record 240e:390:1234::100, got %v", records)
	}
}

func TestCoordinator_ConflictWithExistingDeviceRecords(t *testing.T) {
	tempDir := t.TempDir()
	store := NewFileStateStore(tempDir)
	pub := newMockDNSPublisher()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	prov := &mockAddressProvider{
		addrs: []networkaddr.ReportedIPv6Address{
			{Address: "240e:390:1234::100", Interface: "en0"},
		},
	}
	collector := NewCollector("en0", prov)

	_, err := NewCoordinator(Config{
		Enabled:   true,
		Interface: "en0",
		Records:   []string{"device-a.example.com"},
		ConflictChecker: func(records []string) error {
			for _, r := range records {
				if r == "device-a.example.com" {
					return errors.New("ownership conflict: record already managed by device DDNS")
				}
			}
			return nil
		},
	}, collector, pub, store, logger)

	if err == nil {
		t.Fatalf("expected conflict error, got nil")
	}
}

func TestCoordinator_StartAndStop(t *testing.T) {
	tempDir := t.TempDir()
	store := NewFileStateStore(tempDir)
	pub := newMockDNSPublisher()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	prov := &mockAddressProvider{
		addrs: []networkaddr.ReportedIPv6Address{
			{Address: "240e:390:1234::100", Interface: "en0"},
		},
	}
	collector := NewCollector("en0", prov)

	coord, err := NewCoordinator(Config{
		Enabled:   true,
		Interface: "en0",
		Records:   []string{"hub.example.com"},
		Interval:  20 * time.Millisecond,
		Debounce:  5 * time.Millisecond,
	}, collector, pub, store, logger)
	if err != nil {
		t.Fatalf("failed to create coordinator: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	coord.Start(ctx)
	// Duplicate start should be no-op
	coord.Start(ctx)

	time.Sleep(50 * time.Millisecond)
	coord.Stop()
	// Duplicate stop should be safe
	coord.Stop()
}

func TestCoordinator_ReconcileUpsertFailureAndBackoff(t *testing.T) {
	tempDir := t.TempDir()
	store := NewFileStateStore(tempDir)
	pub := newMockDNSPublisher()
	pub.upsertErr = errors.New("cloudflare api 500 error")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	prov := &mockAddressProvider{
		addrs: []networkaddr.ReportedIPv6Address{
			{Address: "240e:390:1234::100", Interface: "en0"},
		},
	}
	collector := NewCollector("en0", prov)

	coord, _ := NewCoordinator(Config{
		Enabled:    true,
		Interface:  "en0",
		Records:    []string{"hub.example.com"},
		MaxBackoff: 10 * time.Second,
	}, collector, pub, store, logger)

	_ = coord.Reconcile(context.Background())

	state, _ := store.Load()
	rec := state.Records["hub.example.com"]
	if rec.Status != StatusFailed {
		t.Errorf("expected status %s, got %s", StatusFailed, rec.Status)
	}
	if rec.RetryCount != 1 {
		t.Errorf("expected retry count 1, got %d", rec.RetryCount)
	}
	if rec.NextRetryTime == nil {
		t.Errorf("expected non-nil next retry time")
	}

	// Reconcile immediately should be skipped due to backoff
	pub.upsertCalls = 0
	_ = coord.Reconcile(context.Background())
	if pub.upsertCalls != 0 {
		t.Errorf("expected 0 upsert calls during backoff, got %d", pub.upsertCalls)
	}
}

func TestCoordinator_ReconcileVerificationMismatch(t *testing.T) {
	tempDir := t.TempDir()
	store := NewFileStateStore(tempDir)
	pub := newMockDNSPublisher()
	// Upsert succeeds, but GetAAAA returns mismatched IP
	pub.getErr = errors.New("dns timeout")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	prov := &mockAddressProvider{
		addrs: []networkaddr.ReportedIPv6Address{
			{Address: "240e:390:1234::100", Interface: "en0"},
		},
	}
	collector := NewCollector("en0", prov)

	coord, _ := NewCoordinator(Config{
		Enabled:   true,
		Interface: "en0",
		Records:   []string{"hub.example.com"},
	}, collector, pub, store, logger)

	_ = coord.Reconcile(context.Background())

	state, _ := store.Load()
	rec := state.Records["hub.example.com"]
	if rec.Status != StatusFailed {
		t.Errorf("expected status %s on verification failure, got %s", StatusFailed, rec.Status)
	}
}

func TestCoordinator_ReconcileDisabled(t *testing.T) {
	tempDir := t.TempDir()
	store := NewFileStateStore(tempDir)
	pub := newMockDNSPublisher()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	coord, _ := NewCoordinator(Config{
		Enabled:   false,
		Interface: "en0",
		Records:   []string{"hub.example.com"},
	}, nil, pub, store, logger)

	_ = coord.Reconcile(context.Background())

	state, _ := store.Load()
	rec := state.Records["hub.example.com"]
	if rec.Status != StatusDisabled {
		t.Errorf("expected status %s, got %s", StatusDisabled, rec.Status)
	}
}

func TestCoordinator_InvalidConfigValidation(t *testing.T) {
	pub := newMockDNSPublisher()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	_, err := NewCoordinator(Config{
		Enabled:   true,
		Interface: "",
		Records:   []string{"hub.example.com"},
	}, nil, pub, nil, logger)
	if err == nil {
		t.Fatalf("expected error for empty interface")
	}

	_, err = NewCoordinator(Config{
		Enabled:   true,
		Interface: "en0",
		Records:   nil,
	}, nil, pub, nil, logger)
	if err == nil {
		t.Fatalf("expected error for empty records")
	}
}

