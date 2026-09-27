package filestore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"homeagent/internal/store"
)

type ControlPlaneRepository struct {
	mu       sync.Mutex
	path     string
	snapshot *store.ControlPlaneSnapshot
}

func OpenControlPlane(path string) (*ControlPlaneRepository, error) {
	repository := &ControlPlaneRepository{path: path, snapshot: store.NewControlPlaneSnapshot()}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err == nil {
		if err := json.Unmarshal(data, repository.snapshot); err != nil {
			return nil, fmt.Errorf("decode control plane: %w", err)
		}
		if err := store.ValidateControlPlaneSnapshot(repository.snapshot); err != nil {
			return nil, err
		}
	}
	return repository, nil
}

func (repository *ControlPlaneRepository) Load(context.Context) (*store.ControlPlaneSnapshot, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	return cloneSnapshot(repository.snapshot)
}

func (repository *ControlPlaneRepository) Commit(_ context.Context, expectedRevision uint64, next *store.ControlPlaneSnapshot) (*store.ControlPlaneSnapshot, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if repository.snapshot.Revision != expectedRevision {
		return nil, store.ErrRevisionConflict
	}
	candidate, err := cloneSnapshot(next)
	if err != nil {
		return nil, err
	}
	candidate.Revision = expectedRevision + 1
	if err := store.ValidateControlPlaneSnapshot(candidate); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(repository.path), 0700); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(candidate, "", "  ")
	if err != nil {
		return nil, err
	}
	temporary, err := os.OpenFile(repository.path+".tmp", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(repository.path + ".tmp")
		}
	}()
	if _, err := temporary.Write(data); err != nil {
		return nil, err
	}
	if err := temporary.Sync(); err != nil {
		return nil, err
	}
	if err := temporary.Close(); err != nil {
		return nil, err
	}
	if err := os.Rename(repository.path+".tmp", repository.path); err != nil {
		return nil, err
	}
	directory, err := os.Open(filepath.Dir(repository.path))
	if err != nil {
		return nil, err
	}
	if err := directory.Sync(); err != nil && !errors.Is(err, os.ErrInvalid) {
		_ = directory.Close()
		return nil, err
	}
	_ = directory.Close()
	committed = true
	repository.snapshot = candidate
	return cloneSnapshot(candidate)
}

func cloneSnapshot(source *store.ControlPlaneSnapshot) (*store.ControlPlaneSnapshot, error) {
	data, err := json.Marshal(source)
	if err != nil {
		return nil, err
	}
	var clone store.ControlPlaneSnapshot
	if err := json.Unmarshal(data, &clone); err != nil {
		return nil, err
	}
	return &clone, nil
}
