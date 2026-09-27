package domainbinding

import (
	"context"
	"errors"
	"fmt"
	"time"

	"homeagent/internal/store"
)

type Service struct {
	control *store.ControlPlaneService
	now     func() time.Time
}

type CreateCommand struct {
	SourceType       SourceType
	SourceID         string
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

func (service *Service) Create(ctx context.Context, command CreateCommand) (Binding, error) {
	fqdn, err := NormalizeFQDN(command.FQDN, ManagedSuffix)
	if err != nil {
		return Binding{}, err
	}
	if command.SourceType == SourceServer && command.SourceID != LocalServerSourceID {
		return Binding{}, errors.New("invalid server source")
	}
	if command.SourceType != SourceServer && command.SourceType != SourceDevice {
		return Binding{}, errors.New("invalid source type")
	}
	state := ConfigEnabled
	if command.ExistingRecord {
		state = ConfigObserving
	}
	stored := store.DomainBinding{BindingID: newBindingID(service.now()), SourceType: string(command.SourceType), SourceID: command.SourceID, FQDN: fqdn, ConfigState: string(state), RuntimeState: string(RuntimeWaitingReport), Revision: 1, ProviderRecordID: command.ProviderRecordID, TTL: command.TTL, Proxied: command.Proxied, UpdatedAt: service.now()}
	result, controlRevision, err := service.control.CreateDomainBinding(ctx, command.ExpectedRevision, stored)
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

func (service *Service) Enable(ctx context.Context, command EnableCommand) (Binding, error) {
	if !command.PublisherDisabledConfirmed {
		return Binding{}, ErrPublisherConfirmationRequired
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
	return Binding{BindingID: item.BindingID, SourceType: SourceType(item.SourceType), SourceID: item.SourceID, FQDN: item.FQDN, ConfigState: BindingState(item.ConfigState), RuntimeState: RuntimeState(item.RuntimeState), Revision: item.Revision, ControlPlaneRevision: controlRevision, ProviderRecordID: item.ProviderRecordID, TTL: item.TTL, Proxied: item.Proxied, DesiredIPv6: item.DesiredIPv6, ProviderIPv6: item.ProviderIPv6, LastAppliedIPv6: item.LastAppliedIPv6, LastError: item.LastError, LastSyncedAt: item.LastSyncedAt, UpdatedAt: item.UpdatedAt}
}
