package file

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"homeagent/internal/command"
)

func writeUnsafeJSON(path, content string) error {
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return err
	}
	return os.Chmod(path, 0644)
}

func TestDeliveryRepositoryPersistsAndRecovers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deliveries.json")
	r, err := OpenDeliveryRepository(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	d := command.Delivery{ID: "del-1", CommandID: "cmd-1", DeviceID: "dev-1", Status: command.DeliveryPending}
	if err := r.Create(d); err != nil {
		t.Fatal(err)
	}
	stored, err := r.Get(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	leased, err := stored.RenewLease(now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Save(leased, stored.Revision); err != nil {
		t.Fatal(err)
	}
	if err := r.Save(leased, d.Revision); !errors.Is(err, command.ErrConflict) {
		t.Fatalf("expected CAS conflict, got %v", err)
	}
	r2, err := OpenDeliveryRepository(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := r2.Get("del-1")
	if err != nil || got.Status != command.DeliveryLeased || got.Attempt != 1 {
		t.Fatalf("recovery: %+v %v", got, err)
	}
	items, err := r2.ListPending("dev-1", now, 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("pending: %+v %v", items, err)
	}
}

func TestDeliveryRepositoryRejectsUnsafeStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deliveries.json")
	if err := writeUnsafeJSON(path, `{"schema_version":1,"deliveries":[]}`); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenDeliveryRepository(path); err == nil {
		t.Fatal("expected unsafe permissions rejection")
	}
}
