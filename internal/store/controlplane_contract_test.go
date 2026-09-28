package store

import (
	"errors"
	"testing"

	"homeagent/internal/device"
)

func TestValidateControlPlaneSnapshotUniqueConstraints(t *testing.T) {
	tests := []struct {
		name  string
		build func(*ControlPlaneSnapshot)
		want  error
	}{
		{name: "unsupported schema", build: func(snapshot *ControlPlaneSnapshot) { snapshot.SchemaVersion = 2 }},
		{name: "invalid device key", build: func(snapshot *ControlPlaneSnapshot) { snapshot.Devices["wrong"] = &device.Device{ID: "device-1"} }},
		{name: "empty fingerprint", build: func(snapshot *ControlPlaneSnapshot) {
			snapshot.Devices["device-1"] = &device.Device{ID: "device-1", HardwareIdentity: &device.HardwareFingerprint{}}
		}},
		{name: "duplicate fingerprint", want: ErrConflict, build: func(snapshot *ControlPlaneSnapshot) {
			snapshot.Devices["one"] = &device.Device{ID: "one", HardwareIdentity: &device.HardwareFingerprint{Fingerprint: "same"}}
			snapshot.Devices["two"] = &device.Device{ID: "two", HardwareIdentity: &device.HardwareFingerprint{Fingerprint: "same"}}
		}},
		{name: "non-normalized fqdn", build: func(snapshot *ControlPlaneSnapshot) {
			snapshot.Bindings["one"] = &DomainBinding{BindingID: "one", FQDN: "Host.Rokilai.Online.", Revision: 1}
		}},
		{name: "invalid task binding", build: func(snapshot *ControlPlaneSnapshot) {
			snapshot.Tasks["task"] = &ReconcileTask{TaskID: "task", BindingID: "missing"}
		}},
		{name: "duplicate active task", want: ErrConflict, build: func(snapshot *ControlPlaneSnapshot) {
			snapshot.Bindings["binding"] = &DomainBinding{BindingID: "binding", FQDN: "host.rokilai.online", Revision: 1}
			snapshot.Tasks["one"] = &ReconcileTask{TaskID: "one", BindingID: "binding", Status: "pending"}
			snapshot.Tasks["two"] = &ReconcileTask{TaskID: "two", BindingID: "binding", Status: "syncing"}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := NewControlPlaneSnapshot()
			test.build(snapshot)
			err := ValidateControlPlaneSnapshot(snapshot)
			if err == nil {
				t.Fatal("invalid snapshot accepted")
			}
			if test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestValidateControlPlaneSnapshotAllowsCompletedTasksForSameBinding(t *testing.T) {
	snapshot := NewControlPlaneSnapshot()
	snapshot.Bindings["binding"] = &DomainBinding{BindingID: "binding", FQDN: "host.rokilai.online", Revision: 1}
	snapshot.Tasks["one"] = &ReconcileTask{TaskID: "one", BindingID: "binding", Status: "done"}
	snapshot.Tasks["two"] = &ReconcileTask{TaskID: "two", BindingID: "binding", Status: "failed"}
	if err := ValidateControlPlaneSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
}

func TestValidateControlPlaneSnapshotAllowsDeletedBindingFQDNReuse(t *testing.T) {
	snapshot := NewControlPlaneSnapshot()
	snapshot.Bindings["deleted"] = &DomainBinding{BindingID: "deleted", FQDN: "host.rokilai.online", ConfigState: "deleted", Revision: 2}
	snapshot.Bindings["replacement"] = &DomainBinding{BindingID: "replacement", FQDN: "host.rokilai.online", ConfigState: "observing", Revision: 1}
	if err := ValidateControlPlaneSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
}
