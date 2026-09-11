package versionstatus

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"homeagent/internal/githubrelease"
)

// FileRepository persists both channels in one atomically replaced JSON file.
type FileRepository struct{ Path string }

type Repository interface {
	Load() (persistedState, error)
	Save(persistedState) error
}

func (r FileRepository) Load() (persistedState, error) {
	var state persistedState
	data, err := os.ReadFile(r.Path)
	if errors.Is(err, os.ErrNotExist) {
		return persistedState{SchemaVersion: 1, Channels: make(map[githubrelease.Component]Snapshot)}, nil
	}
	if err != nil {
		return state, fmt.Errorf("read version status: %w", err)
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return state, fmt.Errorf("decode version status: %w", err)
	}
	if state.SchemaVersion != 1 || state.Channels == nil {
		return state, errors.New("unsupported version status schema")
	}
	return state, nil
}

func (r FileRepository) Save(state persistedState) error {
	if r.Path == "" {
		return errors.New("version status path is empty")
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode version status: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(r.Path), 0o700); err != nil {
		return fmt.Errorf("create version status directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(r.Path), ".version-status-*")
	if err != nil {
		return fmt.Errorf("create version status temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, r.Path); err != nil {
		return fmt.Errorf("replace version status: %w", err)
	}
	return nil
}
