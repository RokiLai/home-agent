package serverupgrade

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type OperationStatus string

const (
	OperationPrepared   OperationStatus = "prepared"
	OperationReplacing  OperationStatus = "replacing"
	OperationRestarting OperationStatus = "restarting"
	OperationVerifying  OperationStatus = "verifying"
	OperationSucceeded  OperationStatus = "succeeded"
	OperationFailed     OperationStatus = "failed"
	OperationRolledBack OperationStatus = "rolled_back"
)

var (
	ErrOperationInProgress = errors.New("server upgrade operation already in progress")
	ErrOperationNotFound   = errors.New("server upgrade operation not found")
)

type Operation struct {
	ID              string          `json:"operation_id"`
	OwnerID         string          `json:"owner_id"`
	PreviousVersion string          `json:"previous_version"`
	TargetVersion   string          `json:"target_version"`
	Status          OperationStatus `json:"status"`
	ErrorCode       string          `json:"error_code,omitempty"`
	ErrorMessage    string          `json:"error_message,omitempty"`
	RollbackStatus  string          `json:"rollback_status,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

func (o Operation) terminal() bool {
	return o.Status == OperationSucceeded || o.Status == OperationFailed || o.Status == OperationRolledBack
}

type OperationManager struct {
	mu             sync.Mutex
	path           string
	currentVersion string
	operations     map[string]Operation
}

func NewOperationManager(path, currentVersion string) (*OperationManager, error) {
	m := &OperationManager{path: path, currentVersion: currentVersion, operations: make(map[string]Operation)}
	data, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(data, &m.operations); err != nil {
			return nil, fmt.Errorf("decode server upgrade operations: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read server upgrade operations: %w", err)
	}
	changed := false
	for id, operation := range m.operations {
		if (operation.Status == OperationRestarting || operation.Status == OperationVerifying) && operation.TargetVersion == currentVersion {
			operation.Status, operation.UpdatedAt = OperationSucceeded, time.Now().UTC()
			operation.ErrorCode, operation.ErrorMessage = "", ""
			m.operations[id], changed = operation, true
		}
	}
	if changed {
		if err := m.saveLocked(); err != nil {
			return nil, err
		}
	}
	return m, nil
}

func (m *OperationManager) Start(ownerID, targetVersion string) (Operation, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, operation := range m.operations {
		if operation.terminal() {
			continue
		}
		if operation.OwnerID == ownerID && operation.TargetVersion == targetVersion {
			return operation, true, nil
		}
		return Operation{}, false, ErrOperationInProgress
	}
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		return Operation{}, false, fmt.Errorf("generate operation id: %w", err)
	}
	now := time.Now().UTC()
	operation := Operation{ID: "srv-up-" + hex.EncodeToString(idBytes), OwnerID: ownerID, PreviousVersion: m.currentVersion, TargetVersion: targetVersion, Status: OperationPrepared, CreatedAt: now, UpdatedAt: now}
	m.operations[operation.ID] = operation
	if err := m.saveLocked(); err != nil {
		delete(m.operations, operation.ID)
		return Operation{}, false, err
	}
	return operation, false, nil
}

func (m *OperationManager) Get(id string) (Operation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	operation, ok := m.operations[id]
	if !ok {
		return Operation{}, ErrOperationNotFound
	}
	return operation, nil
}

func (m *OperationManager) Run(ctx context.Context, id string, runner func(context.Context, string) error) error {
	if err := m.transitionFrom(id, OperationPrepared, OperationReplacing, "", ""); err != nil {
		return err
	}
	operation, err := m.Get(id)
	if err != nil {
		return err
	}
	if err := runner(ctx, operation.TargetVersion); err != nil {
		_ = m.transition(id, OperationFailed, "upgrade_failed", err.Error())
		return err
	}
	return m.transition(id, OperationRestarting, "", "")
}

func (m *OperationManager) transition(id string, status OperationStatus, errorCode, errorMessage string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	operation, ok := m.operations[id]
	if !ok {
		return ErrOperationNotFound
	}
	operation.Status, operation.ErrorCode, operation.ErrorMessage = status, errorCode, errorMessage
	operation.UpdatedAt = time.Now().UTC()
	m.operations[id] = operation
	return m.saveLocked()
}

func (m *OperationManager) transitionFrom(id string, expected, status OperationStatus, errorCode, errorMessage string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	operation, ok := m.operations[id]
	if !ok {
		return ErrOperationNotFound
	}
	if operation.Status != expected {
		return fmt.Errorf("operation state conflict: current=%s expected=%s", operation.Status, expected)
	}
	operation.Status, operation.ErrorCode, operation.ErrorMessage = status, errorCode, errorMessage
	operation.UpdatedAt = time.Now().UTC()
	m.operations[id] = operation
	return m.saveLocked()
}

func (m *OperationManager) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(m.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m.operations, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(m.path), ".server-upgrade-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
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
	return os.Rename(name, m.path)
}
