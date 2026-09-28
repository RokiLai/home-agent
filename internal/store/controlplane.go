package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"homeagent/internal/auth"
	"homeagent/internal/device"
)

var (
	ErrRevisionConflict = errors.New("control plane revision conflict")
)

type DomainBinding struct {
	BindingID        string     `json:"binding_id"`
	SourceType       string     `json:"source_type"`
	SourceID         string     `json:"source_id"`
	OwnerUserID      string     `json:"owner_user_id,omitempty"`
	FQDN             string     `json:"fqdn"`
	ConfigState      string     `json:"config_state"`
	RuntimeState     string     `json:"runtime_state"`
	Revision         uint64     `json:"revision"`
	ProviderRecordID string     `json:"provider_record_id,omitempty"`
	TTL              int        `json:"ttl,omitempty"`
	Proxied          bool       `json:"proxied"`
	DesiredIPv6      string     `json:"desired_ipv6,omitempty"`
	ProviderIPv6     string     `json:"provider_ipv6,omitempty"`
	LastAppliedIPv6  string     `json:"last_applied_ipv6,omitempty"`
	LastError        string     `json:"last_error,omitempty"`
	LastSyncedAt     *time.Time `json:"last_synced_at,omitempty"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

type ReconcileTask struct {
	TaskID          string    `json:"task_id"`
	BindingID       string    `json:"binding_id"`
	BindingRevision uint64    `json:"binding_revision"`
	DesiredIPv6     string    `json:"desired_ipv6"`
	TaskRevision    uint64    `json:"task_revision"`
	Status          string    `json:"status"`
	Attempts        int       `json:"attempts"`
	NextAttemptAt   time.Time `json:"next_attempt_at,omitempty"`
	LastError       string    `json:"last_error,omitempty"`
}

type ControlPlaneSnapshot struct {
	SchemaVersion int                            `json:"schema_version"`
	Revision      uint64                         `json:"revision"`
	Devices       map[string]*device.Device      `json:"devices"`
	Grants        map[string]*device.DeviceGrant `json:"device_grants"`
	ClaimTokens   map[string]*auth.ClaimToken    `json:"claim_tokens"`
	Bindings      map[string]*DomainBinding      `json:"domain_bindings"`
	Tasks         map[string]*ReconcileTask      `json:"reconcile_tasks"`
}

type ControlPlaneRepository interface {
	Load(ctx context.Context) (*ControlPlaneSnapshot, error)
	Commit(ctx context.Context, expectedRevision uint64, next *ControlPlaneSnapshot) (*ControlPlaneSnapshot, error)
}

func NewControlPlaneSnapshot() *ControlPlaneSnapshot {
	return &ControlPlaneSnapshot{SchemaVersion: 1, Devices: map[string]*device.Device{}, Grants: map[string]*device.DeviceGrant{}, ClaimTokens: map[string]*auth.ClaimToken{}, Bindings: map[string]*DomainBinding{}, Tasks: map[string]*ReconcileTask{}}
}

func ValidateControlPlaneSnapshot(snapshot *ControlPlaneSnapshot) error {
	if snapshot == nil || snapshot.SchemaVersion != 1 {
		return errors.New("unsupported control plane schema")
	}
	fqdns := map[string]string{}
	fingerprints := map[string]string{}
	activeTasks := map[string]string{}
	for id, dev := range snapshot.Devices {
		if dev == nil || id == "" || dev.ID != id {
			return fmt.Errorf("invalid device %q", id)
		}
		if dev.HardwareIdentity != nil {
			key := dev.HardwareIdentity.Fingerprint
			if key == "" {
				return fmt.Errorf("device %q has empty hardware fingerprint", id)
			}
			if owner, ok := fingerprints[key]; ok && owner != id {
				return fmt.Errorf("%w: hardware fingerprint", ErrConflict)
			}
			fingerprints[key] = id
		}
	}
	for id, grant := range snapshot.Grants {
		if grant == nil || id == "" || grant.ID != id || snapshot.Devices[grant.DeviceID] == nil {
			return fmt.Errorf("invalid device grant %q", id)
		}
	}
	for id, binding := range snapshot.Bindings {
		if binding == nil || id == "" || binding.BindingID != id || binding.Revision == 0 {
			return fmt.Errorf("invalid binding %q", id)
		}
		fqdn := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(binding.FQDN), "."))
		if fqdn == "" || fqdn != binding.FQDN {
			return fmt.Errorf("binding %q has non-normalized fqdn", id)
		}
		if binding.ConfigState != "deleted" {
			if owner, ok := fqdns[fqdn]; ok && owner != id {
				return fmt.Errorf("%w: fqdn", ErrConflict)
			}
			fqdns[fqdn] = id
		}
	}
	for id, task := range snapshot.Tasks {
		if task == nil || id == "" || task.TaskID != id || snapshot.Bindings[task.BindingID] == nil {
			return fmt.Errorf("invalid reconcile task %q", id)
		}
		if task.Status == "pending" || task.Status == "syncing" {
			if existing, ok := activeTasks[task.BindingID]; ok && existing != id {
				return fmt.Errorf("%w: active reconcile task", ErrConflict)
			}
			activeTasks[task.BindingID] = id
		}
	}
	return nil
}
