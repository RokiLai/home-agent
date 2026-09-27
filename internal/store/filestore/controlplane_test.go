package filestore

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"homeagent/internal/store"
)

func TestControlPlaneCommitIsDurableAndCASProtected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control-plane.json")
	repository, err := OpenControlPlane(path)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := repository.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Bindings["binding-1"] = &store.DomainBinding{BindingID: "binding-1", SourceType: "server", SourceID: "local-server", FQDN: "rokilai.online", Revision: 1}
	committed, err := repository.Commit(context.Background(), 0, snapshot)
	if err != nil || committed.Revision != 1 {
		t.Fatalf("commit=%+v err=%v", committed, err)
	}
	if _, err := repository.Commit(context.Background(), 0, snapshot); !errors.Is(err, store.ErrRevisionConflict) {
		t.Fatalf("stale commit error = %v", err)
	}
	reopened, err := OpenControlPlane(path)
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := reopened.Load(context.Background())
	if err != nil || reloaded.Bindings["binding-1"] == nil || reloaded.Revision != 1 {
		t.Fatalf("reloaded=%+v err=%v", reloaded, err)
	}
}

func TestControlPlaneRejectsDuplicateFQDNWithoutChangingState(t *testing.T) {
	repository, err := OpenControlPlane(filepath.Join(t.TempDir(), "control-plane.json"))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, _ := repository.Load(context.Background())
	snapshot.Bindings["one"] = &store.DomainBinding{BindingID: "one", FQDN: "a.rokilai.online", Revision: 1}
	snapshot.Bindings["two"] = &store.DomainBinding{BindingID: "two", FQDN: "a.rokilai.online", Revision: 1}
	if _, err := repository.Commit(context.Background(), 0, snapshot); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("error = %v", err)
	}
	after, _ := repository.Load(context.Background())
	if after.Revision != 0 || len(after.Bindings) != 0 {
		t.Fatalf("failed commit mutated state: %+v", after)
	}
}
