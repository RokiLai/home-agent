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

type ControlPlaneService struct {
	repository   ControlPlaneRepository
	offlineAfter time.Duration
}

var (
	ErrHardwareOwnerMismatch = errors.New("hardware identity belongs to another owner")
	ErrHardwareDeviceOnline  = errors.New("hardware identity belongs to an online device")
)

func NewControlPlaneService(repository ControlPlaneRepository) *ControlPlaneService {
	return &ControlPlaneService{repository: repository, offlineAfter: 15 * time.Minute}
}

func (service *ControlPlaneService) SetRecoveryOfflineAfter(duration time.Duration) {
	if duration > 0 {
		service.offlineAfter = duration
	}
}

func (service *ControlPlaneService) CreateClaimToken(ctx context.Context, ttl time.Duration, maxUses int, description, createdBy, ownerID string) (string, *auth.ClaimToken, error) {
	if ttl <= 0 {
		ttl = auth.DefaultClaimTTL
	}
	if maxUses <= 0 {
		maxUses = 1
	}
	raw, err := auth.GenerateSecureToken("claim_", 32)
	if err != nil {
		return "", nil, err
	}
	now := time.Now().UTC()
	token := &auth.ClaimToken{ID: raw[:18], TokenHash: auth.HashToken(raw), CreatedAt: now, ExpiresAt: now.Add(ttl), MaxUses: maxUses, RemainingUses: maxUses, Description: strings.TrimSpace(description), CreatedByUserID: strings.TrimSpace(createdBy), OwnerUserID: strings.TrimSpace(ownerID)}
	_, err = service.update(ctx, func(snapshot *ControlPlaneSnapshot) error {
		snapshot.ClaimTokens[token.TokenHash] = token
		return nil
	})
	if err != nil {
		return "", nil, err
	}
	copy := *token
	return raw, &copy, nil
}

func (service *ControlPlaneService) ListClaimTokens(ctx context.Context) ([]*auth.ClaimToken, error) {
	snapshot, err := service.repository.Load(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	result := []*auth.ClaimToken{}
	for _, token := range snapshot.ClaimTokens {
		if token.RemainingUses <= 0 || now.After(token.ExpiresAt) {
			continue
		}
		copy := *token
		copy.TokenHash = ""
		result = append(result, &copy)
	}
	return result, nil
}

func (service *ControlPlaneService) RevokeClaimToken(ctx context.Context, id string) error {
	_, err := service.update(ctx, func(snapshot *ControlPlaneSnapshot) error {
		for hash, token := range snapshot.ClaimTokens {
			if token.ID == id || strings.HasPrefix(token.ID, id) {
				delete(snapshot.ClaimTokens, hash)
			}
		}
		return nil
	})
	return err
}

func (service *ControlPlaneService) ClaimDevice(ctx context.Context, rawClaimToken, rawDeviceToken string, candidate device.Device) (device.Device, error) {
	tokenHash := auth.HashToken(rawClaimToken)
	candidate.DeviceTokenHash = auth.HashToken(rawDeviceToken)
	var saved device.Device
	_, err := service.update(ctx, func(snapshot *ControlPlaneSnapshot) error {
		token := snapshot.ClaimTokens[tokenHash]
		if token == nil || token.RemainingUses <= 0 || time.Now().UTC().After(token.ExpiresAt) {
			return auth.ErrClaimTokenNotFound
		}
		candidate.OwnerUserID = token.OwnerUserID
		if err := device.Validate(candidate); err != nil {
			return err
		}
		now := time.Now().UTC()
		if candidate.HardwareIdentity != nil {
			for _, existing := range snapshot.Devices {
				if existing.HardwareIdentity == nil || existing.HardwareIdentity.Fingerprint != candidate.HardwareIdentity.Fingerprint {
					continue
				}
				if existing.OwnerUserID != token.OwnerUserID {
					return ErrHardwareOwnerMismatch
				}
				if existing.LastSeenAt.IsZero() || now.Sub(existing.LastSeenAt) <= service.offlineAfter {
					return ErrHardwareDeviceOnline
				}
				candidate.ID = existing.ID
				candidate.CreatedAt = existing.CreatedAt
				candidate.Alias = existing.Alias
				candidate.GitHubSyncEnabled = existing.GitHubSyncEnabled
				break
			}
		}
		if candidate.CreatedAt.IsZero() {
			candidate.CreatedAt = now
		}
		candidate.UpdatedAt, candidate.LastSeenAt = now, now
		copy := candidate
		snapshot.Devices[candidate.ID] = &copy
		token.RemainingUses--
		if token.RemainingUses == 0 {
			delete(snapshot.ClaimTokens, tokenHash)
		}
		saved = copy
		return nil
	})
	return saved, err
}

func (service *ControlPlaneService) ImportLegacy(ctx context.Context, devices []device.Device, grants []device.DeviceGrant, tokens []*auth.ClaimToken) error {
	_, err := service.update(ctx, func(snapshot *ControlPlaneSnapshot) error {
		if len(snapshot.Devices) == 0 {
			for index := range devices {
				copy := devices[index]
				snapshot.Devices[copy.ID] = &copy
			}
		}
		if len(snapshot.Grants) == 0 {
			for index := range grants {
				copy := grants[index]
				snapshot.Grants[copy.ID] = &copy
			}
		}
		if len(snapshot.ClaimTokens) == 0 {
			for _, token := range tokens {
				copy := *token
				snapshot.ClaimTokens[copy.TokenHash] = &copy
			}
		}
		return nil
	})
	return err
}

func (service *ControlPlaneService) ReplaceRegistryState(ctx context.Context, devices []device.Device, grants []device.DeviceGrant) error {
	_, err := service.update(ctx, func(snapshot *ControlPlaneSnapshot) error {
		snapshot.Devices = make(map[string]*device.Device, len(devices))
		for index := range devices {
			copy := devices[index]
			snapshot.Devices[copy.ID] = &copy
		}
		snapshot.Grants = make(map[string]*device.DeviceGrant, len(grants))
		for index := range grants {
			copy := grants[index]
			snapshot.Grants[copy.ID] = &copy
		}
		return nil
	})
	return err
}

func (service *ControlPlaneService) RegistryState(ctx context.Context) ([]device.Device, []device.DeviceGrant, error) {
	snapshot, err := service.repository.Load(ctx)
	if err != nil {
		return nil, nil, err
	}
	devices := make([]device.Device, 0, len(snapshot.Devices))
	for _, item := range snapshot.Devices {
		devices = append(devices, *item)
	}
	grants := make([]device.DeviceGrant, 0, len(snapshot.Grants))
	for _, item := range snapshot.Grants {
		grants = append(grants, *item)
	}
	return devices, grants, nil
}

func (service *ControlPlaneService) Devices(ctx context.Context) ([]device.Device, error) {
	snapshot, err := service.repository.Load(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]device.Device, 0, len(snapshot.Devices))
	for _, item := range snapshot.Devices {
		result = append(result, *item)
	}
	return result, nil
}

func (service *ControlPlaneService) CreateDomainBinding(ctx context.Context, expectedControlRevision uint64, binding DomainBinding) (DomainBinding, uint64, error) {
	snapshot, err := service.repository.Load(ctx)
	if err != nil {
		return DomainBinding{}, 0, err
	}
	if snapshot.Revision != expectedControlRevision {
		return DomainBinding{}, snapshot.Revision, ErrRevisionConflict
	}
	for _, existing := range snapshot.Bindings {
		if existing.FQDN == binding.FQDN && existing.ConfigState != "deleted" {
			return DomainBinding{}, snapshot.Revision, ErrConflict
		}
	}
	copy := binding
	snapshot.Bindings[binding.BindingID] = &copy
	committed, err := service.repository.Commit(ctx, snapshot.Revision, snapshot)
	if err != nil {
		return DomainBinding{}, snapshot.Revision, err
	}
	return copy, committed.Revision, nil
}

func (service *ControlPlaneService) ListDomainBindings(ctx context.Context) ([]DomainBinding, uint64, error) {
	snapshot, err := service.repository.Load(ctx)
	if err != nil {
		return nil, 0, err
	}
	result := make([]DomainBinding, 0, len(snapshot.Bindings))
	for _, binding := range snapshot.Bindings {
		result = append(result, *binding)
	}
	return result, snapshot.Revision, nil
}

func (service *ControlPlaneService) TransitionDomainBinding(ctx context.Context, bindingID string, expectedBindingRevision uint64, configState, runtimeState string, updatedAt time.Time) (DomainBinding, uint64, error) {
	var changed DomainBinding
	committed, err := service.update(ctx, func(snapshot *ControlPlaneSnapshot) error {
		binding := snapshot.Bindings[bindingID]
		if binding == nil {
			return ErrNotFound
		}
		if binding.Revision != expectedBindingRevision {
			return ErrRevisionConflict
		}
		binding.Revision++
		binding.ConfigState = configState
		binding.RuntimeState = runtimeState
		binding.UpdatedAt = updatedAt
		for taskID, task := range snapshot.Tasks {
			if task.BindingID == bindingID && (task.Status == "pending" || task.Status == "syncing") {
				delete(snapshot.Tasks, taskID)
			}
		}
		changed = *binding
		return nil
	})
	if err != nil {
		return DomainBinding{}, 0, err
	}
	return changed, committed.Revision, nil
}

func (service *ControlPlaneService) QueueReconcileTask(ctx context.Context, bindingID string, expectedBindingRevision uint64, desiredIPv6 string, updatedAt time.Time) (ReconcileTask, error) {
	var queued ReconcileTask
	_, err := service.update(ctx, func(snapshot *ControlPlaneSnapshot) error {
		binding := snapshot.Bindings[bindingID]
		if binding == nil {
			return ErrNotFound
		}
		if binding.Revision != expectedBindingRevision {
			return ErrRevisionConflict
		}
		if binding.ConfigState != "enabled" {
			return errors.New("domain binding is not enabled")
		}
		for taskID, task := range snapshot.Tasks {
			if task.BindingID == bindingID && (task.Status == "pending" || task.Status == "syncing") {
				if task.BindingRevision == binding.Revision && task.DesiredIPv6 == desiredIPv6 {
					queued = *task
					return nil
				}
				delete(snapshot.Tasks, taskID)
			}
		}
		binding.DesiredIPv6 = desiredIPv6
		binding.RuntimeState = "pending"
		binding.UpdatedAt = updatedAt
		queued = ReconcileTask{TaskID: "reconcile-" + bindingID + "-" + fmt.Sprint(binding.Revision), BindingID: bindingID, BindingRevision: binding.Revision, DesiredIPv6: desiredIPv6, TaskRevision: binding.Revision, Status: "pending", NextAttemptAt: updatedAt}
		snapshot.Tasks[queued.TaskID] = &queued
		return nil
	})
	return queued, err
}

func (service *ControlPlaneService) UpdateDomainBindingObservation(ctx context.Context, bindingID string, expectedBindingRevision uint64, providerRecordID, providerIPv6 string, ttl int, proxied bool, runtimeState, lastError string, updatedAt time.Time) (DomainBinding, error) {
	var changed DomainBinding
	_, err := service.update(ctx, func(snapshot *ControlPlaneSnapshot) error {
		binding := snapshot.Bindings[bindingID]
		if binding == nil {
			return ErrNotFound
		}
		if binding.Revision != expectedBindingRevision {
			return ErrRevisionConflict
		}
		binding.ProviderRecordID, binding.ProviderIPv6, binding.TTL, binding.Proxied = providerRecordID, providerIPv6, ttl, proxied
		binding.RuntimeState, binding.LastError, binding.UpdatedAt = runtimeState, lastError, updatedAt
		changed = *binding
		return nil
	})
	return changed, err
}

func (service *ControlPlaneService) MarkReconcileTaskSyncing(ctx context.Context, taskID string) (ReconcileTask, error) {
	var changed ReconcileTask
	_, err := service.update(ctx, func(snapshot *ControlPlaneSnapshot) error {
		task := snapshot.Tasks[taskID]
		if task == nil {
			return ErrNotFound
		}
		binding := snapshot.Bindings[task.BindingID]
		if binding == nil || binding.Revision != task.BindingRevision || binding.ConfigState != "enabled" {
			delete(snapshot.Tasks, taskID)
			return ErrRevisionConflict
		}
		task.Status = "syncing"
		binding.RuntimeState = "syncing"
		changed = *task
		return nil
	})
	return changed, err
}

func (service *ControlPlaneService) CompleteReconcileTask(ctx context.Context, taskID string, providerRecordID, providerIPv6 string, ttl int, proxied bool, errorCategory, errorMessage string, retryable bool, completedAt time.Time) error {
	_, err := service.update(ctx, func(snapshot *ControlPlaneSnapshot) error {
		task := snapshot.Tasks[taskID]
		if task == nil {
			return ErrNotFound
		}
		binding := snapshot.Bindings[task.BindingID]
		if binding == nil || binding.Revision != task.BindingRevision || binding.ConfigState != "enabled" || binding.DesiredIPv6 != task.DesiredIPv6 {
			delete(snapshot.Tasks, taskID)
			return ErrRevisionConflict
		}
		if errorMessage == "" {
			binding.ProviderRecordID, binding.ProviderIPv6, binding.LastAppliedIPv6 = providerRecordID, providerIPv6, task.DesiredIPv6
			binding.TTL, binding.Proxied, binding.RuntimeState, binding.LastError = ttl, proxied, "synced", ""
			binding.LastSyncedAt = &completedAt
			delete(snapshot.Tasks, taskID)
			return nil
		}
		binding.RuntimeState, binding.LastError = "failed", errorCategory+": "+errorMessage
		task.Attempts++
		task.LastError = binding.LastError
		if retryable && task.Attempts < 6 {
			delay := time.Minute * time.Duration(1<<min(task.Attempts-1, 4))
			task.Status = "pending"
			task.NextAttemptAt = completedAt.Add(delay)
		} else {
			task.Status = "failed"
		}
		return nil
	})
	return err
}

func (service *ControlPlaneService) RecoverReconcileTasks(ctx context.Context) ([]ReconcileTask, error) {
	result := []ReconcileTask{}
	_, err := service.update(ctx, func(snapshot *ControlPlaneSnapshot) error {
		for taskID, task := range snapshot.Tasks {
			binding := snapshot.Bindings[task.BindingID]
			if binding == nil || binding.ConfigState != "enabled" || binding.Revision != task.BindingRevision {
				delete(snapshot.Tasks, taskID)
				continue
			}
			if task.Status == "syncing" {
				task.Status = "pending"
			}
			if task.Status == "pending" && !task.NextAttemptAt.After(time.Now().UTC()) {
				result = append(result, *task)
			}
		}
		return nil
	})
	return result, err
}

func (service *ControlPlaneService) update(ctx context.Context, mutate func(*ControlPlaneSnapshot) error) (*ControlPlaneSnapshot, error) {
	for attempt := 0; attempt < 4; attempt++ {
		snapshot, err := service.repository.Load(ctx)
		if err != nil {
			return nil, err
		}
		if snapshot.Devices == nil {
			snapshot.Devices = map[string]*device.Device{}
		}
		if snapshot.Grants == nil {
			snapshot.Grants = map[string]*device.DeviceGrant{}
		}
		if snapshot.ClaimTokens == nil {
			snapshot.ClaimTokens = map[string]*auth.ClaimToken{}
		}
		if snapshot.Bindings == nil {
			snapshot.Bindings = map[string]*DomainBinding{}
		}
		if snapshot.Tasks == nil {
			snapshot.Tasks = map[string]*ReconcileTask{}
		}
		if err := mutate(snapshot); err != nil {
			return nil, err
		}
		committed, err := service.repository.Commit(ctx, snapshot.Revision, snapshot)
		if errors.Is(err, ErrRevisionConflict) {
			continue
		}
		return committed, err
	}
	return nil, ErrRevisionConflict
}
