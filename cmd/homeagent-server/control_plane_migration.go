package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type controlPlaneMigrationMarker struct {
	SchemaVersion int       `json:"schema_version"`
	CompletedAt   time.Time `json:"completed_at"`
}

func finalizeControlPlaneMigration(dataDir string) error {
	for _, name := range []string{"devices.json", "enrollment.json"} {
		path := filepath.Join(dataDir, name)
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("read legacy %s: %w", name, err)
		}
		backupPath := path + ".control-plane.bak"
		if existing, readErr := os.ReadFile(backupPath); readErr == nil {
			if !bytes.Equal(existing, data) {
				return fmt.Errorf("legacy backup %s does not match source", name)
			}
			continue
		} else if !os.IsNotExist(readErr) {
			return fmt.Errorf("read legacy backup %s: %w", name, readErr)
		}
		if err := atomicWritePrivateFile(backupPath, data); err != nil {
			return fmt.Errorf("backup legacy %s: %w", name, err)
		}
		verified, err := os.ReadFile(backupPath)
		if err != nil || !bytes.Equal(verified, data) {
			return fmt.Errorf("verify legacy backup %s", name)
		}
	}
	marker, err := json.Marshal(controlPlaneMigrationMarker{SchemaVersion: 1, CompletedAt: time.Now().UTC()})
	if err != nil {
		return err
	}
	return atomicWritePrivateFile(filepath.Join(dataDir, "control-plane.migrated"), marker)
}

func atomicWritePrivateFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".control-plane-migration-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0600); err != nil {
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return err
	}
	committed = true
	return nil
}
