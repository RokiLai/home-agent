package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"homeagent/internal/device"
)

var ErrNotFound = errors.New("device not found")

type fileData struct {
	Devices []device.Device `json:"devices"`
}

type Registry struct {
	mu      sync.RWMutex
	path    string
	devices map[string]device.Device
}

func Open(path string) (*Registry, error) {
	r := &Registry{path: path, devices: map[string]device.Device{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return nil, err
	}
	var data fileData
	if err := json.Unmarshal(b, &data); err != nil {
		return nil, fmt.Errorf("decode registry: %w", err)
	}
	for _, d := range data.Devices {
		d.Addresses = device.FilterAndSortAddresses(d.Addresses)
		r.devices[d.ID] = d
	}
	_ = r.writeLocked()
	return r, nil
}

func (r *Registry) Save(d device.Device) (device.Device, error) {
	if err := device.Validate(d); err != nil {
		return device.Device{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now().UTC()
	if old, ok := r.devices[d.ID]; ok {
		d.CreatedAt = old.CreatedAt
		if d.Alias == "" {
			d.Alias = old.Alias
		}
		if d.SyncStatus == "" {
			d.SyncStatus = old.SyncStatus
		}
		if d.AppliedVersion == 0 {
			d.AppliedVersion = old.AppliedVersion
		}
		if d.AppliedHash == "" {
			d.AppliedHash = old.AppliedHash
		}
		if d.SyncError == "" {
			d.SyncError = old.SyncError
		}
		if d.SyncUpdatedAt.IsZero() {
			d.SyncUpdatedAt = old.SyncUpdatedAt
		}
	} else {
		d.CreatedAt = now
	}
	d.Addresses = device.FilterAndSortAddresses(d.Addresses)
	d.UpdatedAt, d.LastSeenAt = now, now
	old, existed := r.devices[d.ID]
	r.devices[d.ID] = d
	if err := r.writeLocked(); err != nil {
		if existed {
			r.devices[d.ID] = old
		} else {
			delete(r.devices, d.ID)
		}
		return device.Device{}, err
	}
	return d, nil
}

func (r *Registry) List() []device.Device {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]device.Device, 0, len(r.devices))
	for _, d := range r.devices {
		result = append(result, d)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func (r *Registry) Get(id string) (device.Device, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.devices[id]
	if !ok {
		return device.Device{}, ErrNotFound
	}
	return d, nil
}

func (r *Registry) Delete(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	old, ok := r.devices[id]
	if !ok {
		return ErrNotFound
	}
	delete(r.devices, id)
	if err := r.writeLocked(); err != nil {
		r.devices[id] = old
		return err
	}
	return nil
}

func (r *Registry) UpdateAlias(id string, alias string) (device.Device, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.devices[id]
	if !ok {
		return device.Device{}, ErrNotFound
	}
	now := time.Now().UTC()
	d.Alias = strings.TrimSpace(alias)
	d.UpdatedAt = now
	old := r.devices[id]
	r.devices[id] = d
	if err := r.writeLocked(); err != nil {
		r.devices[id] = old
		return device.Device{}, err
	}
	return d, nil
}

func (r *Registry) UpdateSyncStatus(id string, status string, version int64, hash string, errMsg string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.devices[id]
	if !ok {
		return ErrNotFound
	}
	now := time.Now().UTC()
	d.SyncStatus = status
	d.AppliedVersion = version
	d.AppliedHash = hash
	d.SyncError = errMsg
	d.SyncUpdatedAt = now
	d.LastSeenAt = now
	old := r.devices[id]
	r.devices[id] = d
	if err := r.writeLocked(); err != nil {
		r.devices[id] = old
		return err
	}
	return nil
}

func (r *Registry) writeLocked() error {
	if err := os.MkdirAll(filepath.Dir(r.path), 0700); err != nil {
		return err
	}
	data := fileData{Devices: make([]device.Device, 0, len(r.devices))}
	for _, d := range r.devices {
		data.Devices = append(data.Devices, d)
	}
	sort.Slice(data.Devices, func(i, j int) bool { return data.Devices[i].ID < data.Devices[j].ID })
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(r.path), ".devices-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		tmp.Close()
		if !ok {
			os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0600); err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, r.path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(r.path))
	if err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	ok = true
	return nil
}
