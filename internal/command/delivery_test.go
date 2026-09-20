package command

import (
	"errors"
	"testing"
	"time"
)

func TestDeliveryLeaseAndLifecycle(t *testing.T) {
	now := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	c := Command{ID: "cmd-1", DeviceID: "dev-1", Status: StatusQueued}
	d := Delivery{ID: "del-1", CommandID: c.ID, DeviceID: c.DeviceID, Status: DeliveryPending}
	leased, err := d.RenewLease(now, time.Minute)
	if err != nil || leased.Attempt != 1 || !leased.Lease(now) {
		t.Fatalf("lease failed: %+v %v", leased, err)
	}
	if err := CanSend(c, leased, "other-device", now); !errors.Is(err, ErrDeliveryDevice) {
		t.Fatalf("expected device isolation failure, got %v", err)
	}
	sent, err := leased.MarkSent(now, "dev-1:1")
	if err != nil || sent.LastEventID != "dev-1:1" {
		t.Fatalf("send failed: %+v %v", sent, err)
	}
	accepted, err := sent.MarkAccepted(now)
	if err != nil || accepted.Status != DeliveryAccepted {
		t.Fatalf("accept failed: %+v %v", accepted, err)
	}
	completed := accepted.Complete(now, "")
	if !completed.Terminal() || completed.LeaseUntil != nil {
		t.Fatalf("completion retained active lease: %+v", completed)
	}
}

func TestDeliveryRejectsExpiredLeaseAndTerminalCommand(t *testing.T) {
	now := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	c := Command{ID: "cmd-2", DeviceID: "dev-2", Status: StatusQueued}
	d := Delivery{ID: "del-2", CommandID: c.ID, DeviceID: c.DeviceID, Status: DeliveryLeased}
	old := now.Add(-time.Second)
	d.LeaseUntil = &old
	if _, err := d.RenewLease(now, time.Minute); !errors.Is(err, ErrDeliveryExpired) {
		t.Fatalf("expected expired lease, got %v", err)
	}
	if err := CanSend(c, d, c.DeviceID, now); !errors.Is(err, ErrDeliveryExpired) {
		t.Fatalf("expected expired delivery, got %v", err)
	}
	c.Status = StatusCanceled
	d.Status = DeliveryPending
	if err := CanSend(c, d, c.DeviceID, now); !errors.Is(err, ErrDeliveryLease) {
		t.Fatalf("expected terminal command rejection, got %v", err)
	}
}
