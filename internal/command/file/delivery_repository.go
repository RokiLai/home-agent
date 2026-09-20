package file

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"homeagent/internal/command"
)

type deliveryData struct {
	SchemaVersion int                `json:"schema_version"`
	Deliveries    []command.Delivery `json:"deliveries"`
}

// DeliveryRepository 是与 Command 文件仓库分离的投递事实存储，避免旧 schema 被静默解释。
type DeliveryRepository struct {
	mu         sync.Mutex
	path       string
	deliveries map[string]command.Delivery
}

func OpenDeliveryRepository(path string) (*DeliveryRepository, error) {
	r := &DeliveryRepository{path: path, deliveries: map[string]command.Delivery{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return nil, err
	}
	if info, statErr := os.Stat(path); statErr != nil {
		return nil, statErr
	} else if info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("unsafe delivery store permissions %o", info.Mode().Perm())
	}
	var d deliveryData
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, fmt.Errorf("decode deliveries: %w", err)
	}
	if d.SchemaVersion != 1 {
		return nil, fmt.Errorf("unsupported delivery schema %d", d.SchemaVersion)
	}
	for _, item := range d.Deliveries {
		if item.ID == "" || item.CommandID == "" || item.DeviceID == "" {
			return nil, fmt.Errorf("invalid delivery identity")
		}
		r.deliveries[item.ID] = item
	}
	return r, nil
}

func (r *DeliveryRepository) Create(d command.Delivery) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if d.ID == "" || d.CommandID == "" || d.DeviceID == "" {
		return fmt.Errorf("delivery identity is required")
	}
	if _, exists := r.deliveries[d.ID]; exists {
		return fmt.Errorf("delivery %s already exists", d.ID)
	}
	if d.Revision == 0 {
		d.Revision = 1
	}
	r.deliveries[d.ID] = d
	if err := r.writeLocked(); err != nil {
		delete(r.deliveries, d.ID)
		return err
	}
	return nil
}

func (r *DeliveryRepository) Get(id string) (command.Delivery, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.deliveries[id]
	if !ok {
		return command.Delivery{}, command.ErrNotFound
	}
	return d, nil
}

func (r *DeliveryRepository) Save(d command.Delivery, expectedRevision uint64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	old, ok := r.deliveries[d.ID]
	if !ok {
		return command.ErrNotFound
	}
	if old.Revision != expectedRevision {
		return command.ErrConflict
	}
	d.Revision = expectedRevision + 1
	r.deliveries[d.ID] = d
	if err := r.writeLocked(); err != nil {
		r.deliveries[d.ID] = old
		return err
	}
	return nil
}

func (r *DeliveryRepository) ListPending(deviceID string, now time.Time, limit int) ([]command.Delivery, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	items := make([]command.Delivery, 0)
	for _, d := range r.deliveries {
		if d.DeviceID != deviceID || d.Terminal() {
			continue
		}
		if d.Status == command.DeliveryLeased && !d.Lease(now) {
			continue
		}
		items = append(items, d)
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].Attempt < items[j].Attempt || (items[i].Attempt == items[j].Attempt && items[i].ID < items[j].ID)
	})
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func (r *DeliveryRepository) writeLocked() error {
	if err := os.MkdirAll(filepath.Dir(r.path), 0700); err != nil {
		return err
	}
	d := deliveryData{SchemaVersion: 1, Deliveries: make([]command.Delivery, 0, len(r.deliveries))}
	for _, item := range r.deliveries {
		d.Deliveries = append(d.Deliveries, item)
	}
	sort.Slice(d.Deliveries, func(i, j int) bool { return d.Deliveries[i].ID < d.Deliveries[j].ID })
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(r.path), ".deliveries-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err = tmp.Chmod(0600); err == nil {
		_, err = tmp.Write(b)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, r.path)
}
