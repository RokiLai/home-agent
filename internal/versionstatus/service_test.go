package versionstatus

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"homeagent/internal/githubrelease"
)

type fakeFetcher struct {
	releases map[githubrelease.Component]*githubrelease.Release
	errors   map[githubrelease.Component]error
}

func (f *fakeFetcher) GetLatestComponentRelease(_ context.Context, component githubrelease.Component, _ bool) (*githubrelease.Release, error) {
	if err := f.errors[component]; err != nil {
		return nil, err
	}
	return f.releases[component], nil
}

func TestServiceKeepsComponentFailuresIsolatedAndPersists(t *testing.T) {
	now := time.Date(2026, 9, 11, 4, 0, 0, 0, time.UTC)
	fetcher := &fakeFetcher{
		releases: map[githubrelease.Component]*githubrelease.Release{
			githubrelease.ComponentServer: releaseWithAssets(githubrelease.ComponentServer, 10, "server-v0.6.15", "v0.6.15"),
			githubrelease.ComponentAgent:  releaseWithAssets(githubrelease.ComponentAgent, 20, "agent-v0.6.16", "v0.6.16"),
		},
		errors: map[githubrelease.Component]error{},
	}
	path := filepath.Join(t.TempDir(), "version-status.json")
	svc, err := NewService(fetcher, FileRepository{Path: path}, "v0.6.14", func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Refresh(context.Background(), githubrelease.ComponentServer, true); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Refresh(context.Background(), githubrelease.ComponentAgent, true); err != nil {
		t.Fatal(err)
	}

	fetcher.errors[githubrelease.ComponentServer] = errors.New("rate limited")
	now = now.Add(7 * time.Hour)
	server, err := svc.Refresh(context.Background(), githubrelease.ComponentServer, true)
	if err == nil || server.Status != StatusStale || server.LatestVersion != "v0.6.15" || server.ErrorCode == "" {
		t.Fatalf("unexpected stale server snapshot: %+v err=%v", server, err)
	}
	if server.ConsecutiveFailures != 1 || server.RetryAt.Before(now.Add(5*time.Minute)) || !server.RetryAt.Before(now.Add(6*time.Minute)) {
		t.Fatalf("unexpected first retry schedule: %+v", server)
	}
	agent := svc.Snapshot(githubrelease.ComponentAgent)
	if agent.Status != StatusStale || agent.LatestVersion != "v0.6.16" || agent.ErrorCode != "" {
		t.Fatalf("agent channel was polluted: %+v", agent)
	}

	restored, err := NewService(fetcher, FileRepository{Path: path}, "v0.6.14", func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if got := restored.Snapshot(githubrelease.ComponentAgent); got.LatestVersion != "v0.6.16" {
		t.Fatalf("persisted agent snapshot not restored: %+v", got)
	}
}

func TestServiceExpiresUntrustedSnapshotAfterMaxStaleAge(t *testing.T) {
	now := time.Date(2026, 9, 11, 4, 0, 0, 0, time.UTC)
	fetcher := &fakeFetcher{
		releases: map[githubrelease.Component]*githubrelease.Release{
			githubrelease.ComponentServer: releaseWithAssets(githubrelease.ComponentServer, 10, "server-v0.6.15", "v0.6.15"),
		},
		errors: map[githubrelease.Component]error{},
	}
	svc, err := NewService(fetcher, FileRepository{Path: filepath.Join(t.TempDir(), "state.json")}, "v0.6.14", func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Refresh(context.Background(), githubrelease.ComponentServer, true); err != nil {
		t.Fatal(err)
	}
	now = now.Add(25 * time.Hour)
	if got := svc.Snapshot(githubrelease.ComponentServer); got.Status != StatusUnknown || got.LatestVersion != "" || got.UpdateState != UpdateUnknown {
		t.Fatalf("expired snapshot must not participate in decisions: %+v", got)
	}
}

type failingSaveRepository struct{ state persistedState }

func (r *failingSaveRepository) Load() (persistedState, error) { return r.state, nil }
func (r *failingSaveRepository) Save(persistedState) error     { return errors.New("disk full") }

func TestRefreshDoesNotPublishUnpersistedSnapshot(t *testing.T) {
	repo := &failingSaveRepository{state: persistedState{SchemaVersion: 1, Channels: map[githubrelease.Component]Snapshot{}}}
	fetcher := &fakeFetcher{releases: map[githubrelease.Component]*githubrelease.Release{
		githubrelease.ComponentServer: releaseWithAssets(githubrelease.ComponentServer, 10, "server-v0.6.15", "v0.6.15"),
	}, errors: map[githubrelease.Component]error{}}
	svc, err := NewService(fetcher, repo, "v0.6.14", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Refresh(context.Background(), githubrelease.ComponentServer, true); err == nil {
		t.Fatal("persistence failure must fail refresh")
	}
	if got := svc.Snapshot(githubrelease.ComponentServer); got.LatestVersion != "" || got.Status != StatusUnknown {
		t.Fatalf("unpersisted result became visible: %+v", got)
	}
}

func releaseWithAssets(component githubrelease.Component, id int64, tag, componentVersion string) *githubrelease.Release {
	release := &githubrelease.Release{ID: id, TagName: tag, Version: componentVersion, HTMLURL: "https://example/" + string(component)}
	for i, name := range githubrelease.RequiredAssetNames(component) {
		release.Assets = append(release.Assets, githubrelease.Asset{ID: int64(i + 1), Name: name, Size: 10, BrowserDownloadURL: "https://example/" + name})
	}
	return release
}
