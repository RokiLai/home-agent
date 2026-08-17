package networkaddr

import (
	"context"
	"sync"
	"testing"
	"time"
)


type mockProvider struct {
	mu    sync.Mutex
	addrs []ReportedIPv6Address
}

func (m *mockProvider) GetAddresses(ctx context.Context, iface string) ([]ReportedIPv6Address, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	res := make([]ReportedIPv6Address, len(m.addrs))
	copy(res, m.addrs)
	return res, nil
}

func (m *mockProvider) SetAddresses(addrs []ReportedIPv6Address) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.addrs = addrs
}

func TestWatcher_DebounceAndHeartbeat(t *testing.T) {
	prov := &mockProvider{
		addrs: []ReportedIPv6Address{
			{Address: "240e:1::1", Interface: "en0"},
		},
	}

	type event struct {
		snapshot []ReportedIPv6Address
		changed  bool
	}
	var mu sync.Mutex
	var events []event

	watcher := NewWatcher(WatcherConfig{
		Interface:         "en0",
		DebounceDuration:  20 * time.Millisecond,
		HeartbeatInterval: 200 * time.Millisecond,
		PollInterval:      500 * time.Millisecond,
		Provider:          prov,
		OnSnapshot: func(snapshot []ReportedIPv6Address, changed bool) {
			mu.Lock()
			defer mu.Unlock()
			events = append(events, event{snapshot: snapshot, changed: changed})
		},
	})

	watcher.Start()
	defer watcher.Stop()

	// Initial emission should happen immediately
	time.Sleep(10 * time.Millisecond)
	mu.Lock()
	if len(events) < 1 {
		mu.Unlock()
		t.Fatalf("expected initial emission")
	}
	mu.Unlock()

	// Multiple quick triggers with same data should debounce into 0 change emissions
	mu.Lock()
	before := len(events)
	mu.Unlock()

	watcher.Trigger()
	watcher.Trigger()
	watcher.Trigger()
	time.Sleep(60 * time.Millisecond)

	mu.Lock()
	if len(events) != before {
		mu.Unlock()
		t.Fatalf("expected unchanged debounce not to emit, before=%d, after=%d", before, len(events))
	}
	mu.Unlock()

	// Change address and trigger
	prov.SetAddresses([]ReportedIPv6Address{
		{Address: "240e:1::1", Interface: "en0"},
		{Address: "240e:1::2", Interface: "en0"},
	})
	watcher.Trigger()
	time.Sleep(60 * time.Millisecond)

	mu.Lock()
	if len(events) <= before {
		mu.Unlock()
		t.Fatalf("expected emission after address change")
	}
	lastEvent := events[len(events)-1]
	mu.Unlock()

	if !lastEvent.changed {
		t.Errorf("expected changed to be true for new address emission")
	}
	if len(lastEvent.snapshot) != 2 {
		t.Errorf("expected 2 addresses in snapshot, got %d", len(lastEvent.snapshot))
	}
}


func TestParseDarwinIfconfig(t *testing.T) {
	sample := `
en0: flags=8863<UP,BROADCAST,SMART,RUNNING,SIMPLEX,MULTICAST> mtu 1500
	options=6463<RXCSUM,TXCSUM,TSO4,TSO6,CHANNEL_IO>
	ether ac:de:48:00:11:22 
	inet6 fe80::1000:2000:3000:4000%en0 prefixlen 64 scopeid 0x4 
	inet6 240e:abcd:1234:10:1000:2000:3000:4000 prefixlen 64 autoconf secured 
	inet6 240e:abcd:1234:10:5000:6000:7000:8000 prefixlen 64 autoconf temporary 
	inet6 240e:abcd:1234:10:9000:aaaa:bbbb:cccc prefixlen 64 autoconf deprecated 
	inet 192.168.1.50 netmask 0xffffff00 broadcast 192.168.1.255
	nd6 options=201<PERFORMNUD,DAD>
	media: autoselect (1000baseT <full-duplex>)
	status: active
`
	results := ParseDarwinIfconfig(sample, "en0")
	if len(results) != 4 {
		t.Fatalf("expected 4 inet6 lines parsed, got %d", len(results))
	}

	candidates := NormalizeAndFilterCandidates(results, time.Now())
	if len(candidates) != 1 {
		t.Fatalf("expected 1 valid candidate after filtering, got %d", len(candidates))
	}
	if candidates[0].Address != "240e:abcd:1234:10:1000:2000:3000:4000" {
		t.Errorf("expected candidate address 240e:abcd:1234:10:1000:2000:3000:4000, got %s", candidates[0].Address)
	}
}
