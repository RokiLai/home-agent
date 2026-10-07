package domainbinding

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"homeagent/internal/store"
)

type Service struct {
	control  *store.ControlPlaneService
	now      func() time.Time
	provider Provider
}

type CreateCommand struct {
	SourceType       SourceType
	SourceID         string
	OwnerUserID      string
	FQDN             string
	ExpectedRevision uint64
	ExistingRecord   bool
	ProviderRecordID string
	TTL              int
	Proxied          bool
}

type EnableCommand struct {
	BindingID                  string
	ExpectedRevision           uint64
	PublisherDisabledConfirmed bool
}

func NewService(control *store.ControlPlaneService, now func() time.Time) *Service {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Service{control: control, now: now}
}

func (service *Service) AttachProvider(provider Provider) { service.provider = provider }

type PreflightResult struct {
	FQDN        string            `json:"fqdn"`
	Available   bool              `json:"available"`
	Observation RecordObservation `json:"observation"`
}

func (service *Service) Preflight(ctx context.Context, fqdn string) (PreflightResult, error) {
	normalized, err := NormalizeFQDN(fqdn, ManagedSuffix)
	if err != nil {
		return PreflightResult{}, err
	}
	bindings, err := service.List(ctx, "", "")
	if err != nil {
		return PreflightResult{}, err
	}
	for _, binding := range bindings {
		if binding.FQDN == normalized {
			return PreflightResult{FQDN: normalized}, nil
		}
	}
	result := PreflightResult{FQDN: normalized, Available: true}
	if service.provider != nil {
		result.Observation, err = service.provider.Observe(ctx, ObserveRequest{FQDN: normalized})
		if err != nil {
			return PreflightResult{}, err
		}
	}
	return result, nil
}

func (service *Service) Create(ctx context.Context, command CreateCommand) (Binding, error) {
	preflight, err := service.Preflight(ctx, command.FQDN)
	if err != nil {
		return Binding{}, err
	}
	if !preflight.Available {
		return Binding{}, ErrFQDNConflict
	}
	fqdn := preflight.FQDN
	if command.SourceType == SourceServer && command.SourceID != LocalServerSourceID {
		return Binding{}, errors.New("invalid server source")
	}
	if command.SourceType != SourceServer && command.SourceType != SourceDevice {
		return Binding{}, errors.New("invalid source type")
	}
	state := ConfigEnabled
	if preflight.Observation.Exists || (service.provider == nil && command.ExistingRecord) {
		state = ConfigObserving
	}
	recordID, ttl, proxied, providerIPv6 := command.ProviderRecordID, command.TTL, command.Proxied, ""
	if service.provider != nil {
		recordID, ttl, proxied, providerIPv6 = preflight.Observation.RecordID, preflight.Observation.TTL, preflight.Observation.Proxied, preflight.Observation.IPv6
	}
	stored := store.DomainBinding{BindingID: newBindingID(service.now()), SourceType: string(command.SourceType), SourceID: command.SourceID, OwnerUserID: strings.TrimSpace(command.OwnerUserID), FQDN: fqdn, ConfigState: string(state), RuntimeState: string(RuntimeWaitingReport), Revision: 1, ProviderRecordID: recordID, TTL: ttl, Proxied: proxied, ProviderIPv6: providerIPv6, UpdatedAt: service.now()}
	result, controlRevision, err := service.control.CreateDomainBinding(ctx, command.ExpectedRevision, stored)
	if errors.Is(err, store.ErrRevisionConflict) {
		return Binding{}, ErrBindingRevisionConflict
	}
	if errors.Is(err, store.ErrConflict) {
		return Binding{}, ErrFQDNConflict
	}
	return fromStore(result, controlRevision), err
}

func (service *Service) List(ctx context.Context, sourceType SourceType, sourceID string) ([]Binding, error) {
	items, revision, err := service.control.ListDomainBindings(ctx)
	if err != nil {
		return nil, err
	}
	result := []Binding{}
	for _, item := range items {
		if (sourceType == "" || item.SourceType == string(sourceType)) && (sourceID == "" || item.SourceID == sourceID) && item.ConfigState != string(ConfigDeleted) {
			result = append(result, fromStore(item, revision))
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].FQDN == result[j].FQDN {
			return result[i].BindingID < result[j].BindingID
		}
		return result[i].FQDN < result[j].FQDN
	})
	return result, nil
}

func (service *Service) ControlPlaneRevision(ctx context.Context) (uint64, error) {
	_, revision, err := service.control.ListDomainBindings(ctx)
	return revision, err
}

func (service *Service) QueueDesiredAddress(ctx context.Context, bindingID string, revision uint64, desiredIPv6 string) (store.ReconcileTask, error) {
	return service.control.QueueReconcileTask(ctx, bindingID, revision, desiredIPv6, service.now())
}

func (service *Service) Recover(ctx context.Context) ([]store.ReconcileTask, error) {
	return service.control.RecoverReconcileTasks(ctx)
}

func (service *Service) RecordObservation(ctx context.Context, binding Binding, observation RecordObservation, runtime RuntimeState, lastError string) (Binding, error) {
	item, err := service.control.UpdateDomainBindingObservation(ctx, binding.BindingID, binding.Revision, observation.RecordID, observation.IPv6, observation.TTL, observation.Proxied, string(runtime), lastError, service.now())
	return fromStore(item, binding.ControlPlaneRevision), err
}

func (service *Service) MarkTaskSyncing(ctx context.Context, taskID string) (store.ReconcileTask, error) {
	return service.control.MarkReconcileTaskSyncing(ctx, taskID)
}

func (service *Service) CompleteTask(ctx context.Context, taskID string, observation RecordObservation, reconcileErr error) error {
	category, message, retryable := "", "", false
	if reconcileErr != nil {
		message = reconcileErr.Error()
		var classified ClassifiedError
		if errors.As(reconcileErr, &classified) {
			category, retryable = classified.Category(), classified.CanRetry()
		} else {
			category = "internal"
		}
	}
	return service.control.CompleteReconcileTask(ctx, taskID, observation.RecordID, observation.IPv6, observation.TTL, observation.Proxied, category, message, retryable, service.now())
}

func (service *Service) Enable(ctx context.Context, command EnableCommand) (Binding, error) {
	if !command.PublisherDisabledConfirmed {
		return Binding{}, ErrPublisherConfirmationRequired
	}
	if service.provider != nil {
		bindings, err := service.List(ctx, "", "")
		if err != nil {
			return Binding{}, err
		}
		var current *Binding
		for index := range bindings {
			if bindings[index].BindingID == command.BindingID {
				current = &bindings[index]
				break
			}
		}
		if current == nil {
			return Binding{}, ErrBindingNotFound
		}
		if current.Revision != command.ExpectedRevision {
			return Binding{}, ErrBindingRevisionConflict
		}
		observation, err := service.provider.Observe(ctx, ObserveRequest{FQDN: current.FQDN, ProviderRecordID: current.ProviderRecordID})
		if err != nil {
			return Binding{}, err
		}
		if current.ConfigState == ConfigObserving && !observation.Exists {
			return Binding{}, errors.New("provider record disappeared before enable")
		}
		if current.ConfigState == ConfigDisabled && observation.Exists {
			adopted, err := service.RecordObservation(ctx, *current, observation, current.RuntimeState, current.LastError)
			if err != nil {
				return Binding{}, err
			}
			current = &adopted
		}
	}
	return service.transition(ctx, command.BindingID, command.ExpectedRevision, ConfigEnabled)
}

func (service *Service) Disable(ctx context.Context, bindingID string, revision uint64) (Binding, error) {
	return service.transition(ctx, bindingID, revision, ConfigDisabled)
}

func (service *Service) Delete(ctx context.Context, bindingID string, revision uint64) (Binding, error) {
	return service.transition(ctx, bindingID, revision, ConfigDeleted)
}

func (service *Service) transition(ctx context.Context, bindingID string, expected uint64, state BindingState) (Binding, error) {
	item, controlRevision, err := service.control.TransitionDomainBinding(ctx, bindingID, expected, string(state), string(RuntimeWaitingReport), service.now())
	if errors.Is(err, store.ErrRevisionConflict) {
		return Binding{}, ErrBindingRevisionConflict
	}
	if errors.Is(err, store.ErrNotFound) {
		return Binding{}, ErrBindingNotFound
	}
	return fromStore(item, controlRevision), err
}

func newBindingID(now time.Time) string { return fmt.Sprintf("binding-%d", now.UnixNano()) }

func fromStore(item store.DomainBinding, controlRevision uint64) Binding {
	return Binding{BindingID: item.BindingID, SourceType: SourceType(item.SourceType), SourceID: item.SourceID, OwnerUserID: item.OwnerUserID, FQDN: item.FQDN, ConfigState: BindingState(item.ConfigState), RuntimeState: RuntimeState(item.RuntimeState), Revision: item.Revision, ControlPlaneRevision: controlRevision, ProviderRecordID: item.ProviderRecordID, TTL: item.TTL, Proxied: item.Proxied, DesiredIPv6: item.DesiredIPv6, ProviderIPv6: item.ProviderIPv6, LastAppliedIPv6: item.LastAppliedIPv6, LastError: item.LastError, LastSyncedAt: item.LastSyncedAt, UpdatedAt: item.UpdatedAt}
}
