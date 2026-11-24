package domainbinding

import (
	"context"
	"errors"
	"sync"
	"time"

	"homeagent/internal/store"
)

var (
	ErrNoDesiredAddress   = errors.New("no adjudicated desired IPv6")
	ErrSourceUnauthorized = errors.New("binding owner no longer has source permission")
)

type SourceResolver interface {
	DesiredIPv6(context.Context, Binding) (string, error)
}

type Coordinator struct {
	service  *Service
	provider Provider
	resolver SourceResolver
	mu       sync.Mutex
	storeMu  sync.Mutex
}

const maxReconcileConcurrency = 8

func NewCoordinator(service *Service, provider Provider, resolver SourceResolver) *Coordinator {
	return &Coordinator{service: service, provider: provider, resolver: resolver}
}

func (coordinator *Coordinator) RunOnce(ctx context.Context) error {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	bindings, err := coordinator.service.List(ctx, "", "")
	if err != nil {
		return err
	}
	for _, binding := range bindings {
		switch binding.ConfigState {
		case ConfigObserving:
			observation, observeErr := coordinator.provider.Observe(ctx, ObserveRequest{FQDN: binding.FQDN, ProviderRecordID: binding.ProviderRecordID})
			if observeErr != nil {
				_, _ = coordinator.service.RecordObservation(ctx, binding, RecordObservation{}, RuntimeFailed, safeError(observeErr))
				continue
			}
			_, _ = coordinator.service.RecordObservation(ctx, binding, observation, RuntimeWaitingReport, "")
		case ConfigEnabled:
			desired, resolveErr := coordinator.resolver.DesiredIPv6(ctx, binding)
			if errors.Is(resolveErr, ErrSourceUnauthorized) {
				_, _ = coordinator.service.Disable(ctx, binding.BindingID, binding.Revision)
				continue
			}
			if resolveErr != nil || desired == "" {
				runtime := RuntimeWaitingReport
				if binding.LastAppliedIPv6 != "" {
					runtime = RuntimeStale
				}
				message := "no_desired_address"
				if resolveErr != nil && !errors.Is(resolveErr, ErrNoDesiredAddress) {
					message = "source_unavailable"
				}
				if binding.RuntimeState != runtime || binding.LastError != message {
					_, _ = coordinator.service.RecordObservation(ctx, binding, observationFromBinding(binding), runtime, message)
				}
				continue
			}
			if desired != binding.LastAppliedIPv6 || binding.RuntimeState != RuntimeSynced {
				_, _ = coordinator.service.QueueDesiredAddress(ctx, binding.BindingID, binding.Revision, desired)
			}
		}
	}
	tasks, err := coordinator.service.Recover(ctx)
	if err != nil {
		return err
	}
	if len(tasks) == 0 {
		return nil
	}
	workers := len(tasks)
	if workers > maxReconcileConcurrency {
		workers = maxReconcileConcurrency
	}
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	var errMu sync.Mutex
	var firstErr error
	for _, task := range tasks {
		task := task
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if err := coordinator.process(ctx, task); err != nil && !errors.Is(err, store.ErrRevisionConflict) && !errors.Is(err, store.ErrNotFound) {
				errMu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				errMu.Unlock()
			}
		}()
	}
	wg.Wait()
	return firstErr
}

func observationFromBinding(binding Binding) RecordObservation {
	return RecordObservation{Exists: binding.ProviderRecordID != "", RecordID: binding.ProviderRecordID, IPv6: binding.ProviderIPv6, TTL: binding.TTL, Proxied: binding.Proxied}
}

func (coordinator *Coordinator) process(ctx context.Context, task store.ReconcileTask) error {
	coordinator.storeMu.Lock()
	if _, err := coordinator.service.MarkTaskSyncing(ctx, task.TaskID); err != nil {
		coordinator.storeMu.Unlock()
		return err
	}
	bindings, err := coordinator.service.List(ctx, "", "")
	coordinator.storeMu.Unlock()
	if err != nil {
		return err
	}
	var binding *Binding
	for index := range bindings {
		if bindings[index].BindingID == task.BindingID {
			binding = &bindings[index]
			break
		}
	}
	if binding == nil || binding.Revision != task.BindingRevision {
		return store.ErrRevisionConflict
	}
	observation, reconcileErr := NewReconciler(coordinator.provider).Reconcile(ctx, *binding)
	coordinator.storeMu.Lock()
	defer coordinator.storeMu.Unlock()
	if reconcileErr != nil && observation.RecordID != "" {
		if _, err := coordinator.service.RecordObservation(ctx, *binding, observation, RuntimeFailed, safeError(reconcileErr)); err != nil {
			return err
		}
	}
	return coordinator.service.CompleteTask(ctx, task.TaskID, observation, reconcileErr)
}

func (coordinator *Coordinator) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	_ = coordinator.RunOnce(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = coordinator.RunOnce(ctx)
		}
	}
}

func safeError(err error) string {
	var classified ClassifiedError
	if errors.As(err, &classified) {
		return classified.Category()
	}
	return "provider_error"
}
