package prefixstate

import (
	"errors"
	"net/netip"
	"testing"
	"time"

	"homeagent/internal/networkaddr"
)

func TestPrefixNormalizationAndIntersection(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	future := now.Add(24 * time.Hour)

	prefA := ReportedIPv6Prefix{
		Prefix:         "240e:1234:5678:10::/64",
		PreferredUntil: &future,
	}
	prefB := ReportedIPv6Prefix{
		Prefix:         "240e:1234:5678:20::/64",
		PreferredUntil: &future,
	}
	prefULA := ReportedIPv6Prefix{
		Prefix: "fd00:1234::/64",
	}

	norm := NormalizePrefixes([]ReportedIPv6Prefix{prefA, prefB, prefULA}, now)
	if len(norm) != 2 {
		t.Fatalf("expected 2 valid prefixes, got %d", len(norm))
	}

	activePrefixes := []netip.Prefix{
		netip.MustParsePrefix("240e:1234:5678:10::/64"),
		netip.MustParsePrefix("240e:1234:5678:20::/64"),
	}

	candidates := []networkaddr.ReportedIPv6Address{
		{Address: "240e:1234:5678:10::100"},
		{Address: "240e:1234:5678:20::200"},
		{Address: "240e:9999:9999:99::999"}, // outside prefix
	}

	intersected := Intersect(candidates, activePrefixes)
	if len(intersected) != 2 {
		t.Fatalf("expected 2 intersected addresses, got %d", len(intersected))
	}
}

func TestDualPrefixTransition_A_AB_B(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	future := now.Add(24 * time.Hour)

	addrA := netip.MustParseAddr("240e:10::1")
	addrB := netip.MustParseAddr("240e:20::2")

	prefA := []netip.Prefix{netip.MustParsePrefix("240e:10::/64")}
	prefAB := []netip.Prefix{netip.MustParsePrefix("240e:10::/64"), netip.MustParsePrefix("240e:20::/64")}
	prefB := []netip.Prefix{netip.MustParsePrefix("240e:20::/64")}

	_ = future

	// Phase 1: Only A is active
	candA := []networkaddr.ReportedIPv6Address{{Address: addrA.String()}}
	resA := Intersect(candA, prefA)
	selA, ok := SelectAddress(resA, "")
	if !ok || selA != addrA {
		t.Fatalf("Phase 1 failed: expected %s, got %s", addrA, selA)
	}

	// Phase 2: Dual prefix transition (A + B active, device has A + B)
	// Server should keep applied addrA to prevent DNS flapping!
	candAB := []networkaddr.ReportedIPv6Address{{Address: addrA.String()}, {Address: addrB.String()}}
	resAB := Intersect(candAB, prefAB)
	selAB, ok := SelectAddress(resAB, addrA.String())
	if !ok || selAB != addrA {
		t.Fatalf("Phase 2 failed: expected stability keeping %s, got %s", addrA, selAB)
	}

	// Phase 3: Router dropped prefix A, only B is active.
	// Even if device still reports candidate A and applied is A, Server must switch to B!
	resB := Intersect(candAB, prefB)
	selB, ok := SelectAddress(resB, addrA.String())
	if !ok || selB != addrB {
		t.Fatalf("Phase 3 failed: expected switch to %s, got %s", addrB, selB)
	}
}

func TestParseUbusInterfaceStatus(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	sample := `{
		"up": true,
		"ipv6-prefix-assignment": [
			{
				"address": "240e:1234:5678:10::",
				"mask": 64,
				"preferred": 3600,
				"valid": 7200
			}
		]
	}`

	prefixes, err := ParseUbusInterfaceStatus([]byte(sample), now)
	if err != nil {
		t.Fatalf("failed to parse ubus status: %v", err)
	}
	if len(prefixes) != 1 {
		t.Fatalf("expected 1 prefix, got %d", len(prefixes))
	}
	if prefixes[0].Prefix != "240e:1234:5678:10::/64" {
		t.Errorf("expected 240e:1234:5678:10::/64, got %s", prefixes[0].Prefix)
	}
	if prefixes[0].PreferredUntil == nil || !prefixes[0].PreferredUntil.Equal(now.Add(3600*time.Second)) {
		t.Errorf("preferred until mismatch: %v", prefixes[0].PreferredUntil)
	}
}

func TestPrefixService_RevisionsAndStale(t *testing.T) {
	svc := NewService(nil)
	t0 := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)

	pList1 := []ReportedIPv6Prefix{
		{Prefix: "240e:1234:5678:10::/64"},
	}

	// 1. Initial snapshot
	st, changed, err := svc.UpdateRouterPrefixes("router-1", "home", 1, t0, pList1)
	if err != nil || !changed {
		t.Fatalf("expected changed=true on initial insert, err=%v", err)
	}
	if st.Revision != 1 {
		t.Errorf("expected revision=1")
	}

	// 2. Idempotent same revision
	_, changed2, err := svc.UpdateRouterPrefixes("router-1", "home", 1, t0, pList1)
	if err != nil || changed2 {
		t.Fatalf("expected changed=false on idempotent update")
	}

	// 3. Older revision conflict
	_, _, err = svc.UpdateRouterPrefixes("router-1", "home", 0, t0, pList1)
	if !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("expected ErrRevisionConflict, got %v", err)
	}

	// 4. Stale check
	prefs, isStale, err := svc.GetActivePrefixes("home", t0.Add(30*time.Minute), 15*time.Minute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isStale {
		t.Errorf("expected isStale=true after 30 minutes with 15m TTL")
	}
	if len(prefs) != 1 {
		t.Errorf("expected 1 prefix even if stale")
	}
}
