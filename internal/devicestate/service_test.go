package devicestate

import (
	"errors"
	"testing"
	"time"

	"homeagent/internal/networkaddr"
)

func TestService_UpdateReportedAddresses(t *testing.T) {
	svc := NewService(nil)
	t0 := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)

	addrs1 := []networkaddr.ReportedIPv6Address{
		{Address: "2001:db8:1::1", Interface: "en0"},
	}

	// 1. Initial snapshot with revision 1
	st, changed, err := svc.UpdateReportedAddresses("dev-1", "net-1", 1, t0, addrs1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !changed {
		t.Errorf("expected changed=true for initial insert")
	}
	if st.Revision != 1 || st.SyncStatus != SyncStatusPending {
		t.Errorf("expected revision=1, status=pending, got rev=%d, status=%s", st.Revision, st.SyncStatus)
	}

	// 2. Same revision, same content -> idempotent success (changed=false)
	st2, changed2, err := svc.UpdateReportedAddresses("dev-1", "net-1", 1, t0, addrs1)
	if err != nil {
		t.Fatalf("unexpected error on idempotent update: %v", err)
	}
	if changed2 {
		t.Errorf("expected changed=false on idempotent update")
	}
	if st2.Revision != 1 {
		t.Errorf("expected revision=1")
	}

	// 3. Same revision, different content -> ErrRevisionContentMismatch
	addrs2 := []networkaddr.ReportedIPv6Address{
		{Address: "2001:db8:1::2", Interface: "en0"},
	}
	_, _, err = svc.UpdateReportedAddresses("dev-1", "net-1", 1, t0, addrs2)
	if !errors.Is(err, ErrRevisionContentMismatch) {
		t.Fatalf("expected ErrRevisionContentMismatch, got %v", err)
	}

	// 4. Older revision -> ErrRevisionConflict
	_, _, err = svc.UpdateReportedAddresses("dev-1", "net-1", 0, t0, addrs1)
	if !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("expected ErrRevisionConflict, got %v", err)
	}

	// 5. Higher revision with new address -> changed=true
	st3, changed3, err := svc.UpdateReportedAddresses("dev-1", "net-1", 2, t0.Add(time.Minute), addrs2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !changed3 {
		t.Errorf("expected changed=true for new revision with new address")
	}
	if st3.Revision != 2 || st3.ReportedAddresses[0].Address != "2001:db8:1::2" {
		t.Errorf("expected revision=2 and new address, got %+v", st3)
	}
}

func TestRevisionConflictCarriesAtomicSnapshot(t *testing.T) {
	svc := NewService(nil)
	now := time.Now().UTC()
	if _, _, err := svc.UpdateReportedAddresses("dev-1", "home", 9, now, nil); err != nil {
		t.Fatal(err)
	}
	_, _, err := svc.UpdateReportedAddresses("dev-1", "home", 1, now, nil)
	var conflict *RevisionConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("error = %v, want RevisionConflictError", err)
	}
	if conflict.Kind != RevisionConflictOlder || conflict.Received != 1 || conflict.Current != 9 {
		t.Fatalf("conflict = %+v", conflict)
	}
}
