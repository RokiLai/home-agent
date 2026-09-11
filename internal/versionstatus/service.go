package versionstatus

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"homeagent/internal/githubrelease"
)

type Fetcher interface {
	GetLatestComponentRelease(context.Context, githubrelease.Component, bool) (*githubrelease.Release, error)
}

type Service struct {
	mu            sync.RWMutex
	fetcher       Fetcher
	repository    Repository
	serverVersion string
	now           func() time.Time
	channels      map[githubrelease.Component]Snapshot
	refreshLocks  map[githubrelease.Component]*sync.Mutex
}

func NewService(fetcher Fetcher, repository Repository, serverVersion string, now func() time.Time) (*Service, error) {
	if fetcher == nil {
		return nil, fmt.Errorf("version release fetcher is required")
	}
	if now == nil {
		now = time.Now
	}
	state, err := repository.Load()
	if err != nil {
		return nil, err
	}
	return &Service{
		fetcher: fetcher, repository: repository, serverVersion: serverVersion, now: now, channels: state.Channels,
		refreshLocks: map[githubrelease.Component]*sync.Mutex{
			githubrelease.ComponentServer: {},
			githubrelease.ComponentAgent:  {},
		},
	}, nil
}

func (s *Service) Snapshot(component githubrelease.Component) Snapshot {
	s.mu.RLock()
	snapshot, ok := s.channels[component]
	s.mu.RUnlock()
	if !ok {
		return Snapshot{Component: component, Status: StatusUnknown, UpdateState: UpdateUnknown}
	}
	return s.withAge(snapshot)
}

func (s *Service) withAge(snapshot Snapshot) Snapshot {
	if snapshot.LastSuccessAt.IsZero() {
		return snapshot
	}
	age := s.now().Sub(snapshot.LastSuccessAt)
	if age > 24*time.Hour {
		snapshot.Status, snapshot.LatestVersion, snapshot.UpdateState = StatusUnknown, "", UpdateUnknown
		snapshot.Stale = false
		return snapshot
	}
	if age > 6*time.Hour {
		snapshot.Status, snapshot.Stale = StatusStale, true
	}
	return snapshot
}

func (s *Service) Refresh(ctx context.Context, component githubrelease.Component, force bool) (Snapshot, error) {
	refreshLock, ok := s.refreshLocks[component]
	if !ok {
		return Snapshot{}, fmt.Errorf("unsupported component %q", component)
	}
	refreshLock.Lock()
	defer refreshLock.Unlock()
	if !force {
		current := s.Snapshot(component)
		if !current.CheckedAt.IsZero() && s.now().Sub(current.CheckedAt) < 10*time.Minute {
			return current, nil
		}
	}
	rel, fetchErr := s.fetcher.GetLatestComponentRelease(ctx, component, force)
	if fetchErr == nil {
		if err := githubrelease.ValidateReleaseAssets(component, rel); err != nil {
			fetchErr = fmt.Errorf("artifact manifest incomplete: %w", err)
		}
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot := s.channels[component]
	snapshot.Component = component
	snapshot.CheckedAt = now
	if component == githubrelease.ComponentServer {
		snapshot.CurrentVersion = s.serverVersion
	}
	if fetchErr != nil {
		snapshot.ConsecutiveFailures++
		snapshot.ErrorCode = classifyError(fetchErr)
		snapshot.ErrorMessage = fetchErr.Error()
		snapshot.RetryAt = now.Add(retryDelay(snapshot.ConsecutiveFailures, now))
		if !snapshot.LastSuccessAt.IsZero() && now.Sub(snapshot.LastSuccessAt) <= 24*time.Hour {
			snapshot.Status, snapshot.Stale = StatusStale, true
		} else {
			snapshot.Status, snapshot.LatestVersion, snapshot.UpdateState = StatusError, "", UpdateUnknown
		}
	} else {
		snapshot.Status, snapshot.Stale = StatusAvailable, false
		snapshot.SnapshotID = fmt.Sprintf("%s:%d:%d", component, rel.ID, now.Unix())
		snapshot.LatestVersion, snapshot.LastSuccessAt = rel.Version, now
		snapshot.ReleaseURL, snapshot.ReleaseID = rel.HTMLURL, rel.ID
		snapshot.Tag, snapshot.Commitish = rel.TagName, rel.Commitish
		snapshot.Assets = append([]githubrelease.Asset(nil), rel.Assets...)
		snapshot.ErrorCode, snapshot.ErrorMessage = "", ""
		snapshot.ConsecutiveFailures = 0
		snapshot.RetryAt = time.Time{}
		if component == githubrelease.ComponentServer {
			if githubrelease.CompareVersions(s.serverVersion, rel.Version) < 0 {
				snapshot.UpdateState = UpdateAvailable
			} else {
				snapshot.UpdateState = UpdateCurrent
			}
		} else {
			snapshot.UpdateState = ""
		}
	}
	nextChannels := make(map[githubrelease.Component]Snapshot, len(s.channels)+1)
	for key, value := range s.channels {
		nextChannels[key] = value
	}
	nextChannels[component] = snapshot
	if err := s.repository.Save(persistedState{SchemaVersion: 1, Channels: nextChannels}); err != nil {
		return snapshot, fmt.Errorf("persist version snapshot: %w", err)
	}
	s.channels = nextChannels
	return snapshot, fetchErr
}

func retryDelay(failures int, now time.Time) time.Duration {
	steps := []time.Duration{5 * time.Minute, 15 * time.Minute, 30 * time.Minute, time.Hour}
	index := failures - 1
	if index < 0 {
		index = 0
	}
	if index >= len(steps) {
		index = len(steps) - 1
	}
	base := steps[index]
	jitter := time.Duration(now.UnixNano() % int64(base/5))
	return base + jitter
}

// Due reports whether the scheduler should refresh a component now.
func (s *Service) Due(component githubrelease.Component) bool {
	snapshot := s.Snapshot(component)
	now := s.now()
	if snapshot.CheckedAt.IsZero() {
		return true
	}
	if !snapshot.RetryAt.IsZero() {
		return !now.Before(snapshot.RetryAt)
	}
	return snapshot.LastSuccessAt.IsZero() || now.Sub(snapshot.LastSuccessAt) >= 6*time.Hour
}

func classifyError(err error) string {
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "pagination incomplete") {
		return "incomplete"
	}
	if strings.Contains(message, "status 403") || strings.Contains(message, "status 429") || strings.Contains(message, "rate limit") {
		return "rate_limited"
	}
	if strings.Contains(message, "no valid") {
		return "migration_required"
	}
	if strings.Contains(message, "artifact manifest incomplete") {
		return "artifact_manifest_incomplete"
	}
	return "github_unavailable"
}
