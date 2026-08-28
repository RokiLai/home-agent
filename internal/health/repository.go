// Package health 提供健康快照与历史事件的原子文件存储与查询。
package health

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
)

// ErrNotFound 当查询不存在的健康快照时返回此错误。
var ErrNotFound = errors.New("health snapshot not found")

// Repository 定义健康数据存储契约。
type Repository interface {
	SaveSnapshot(ctx context.Context, snapshot HealthSnapshot) error
	GetSnapshot(ctx context.Context, deviceID string) (*HealthSnapshot, error)
	ListSnapshots(ctx context.Context) ([]HealthSnapshot, error)
	AppendEvents(ctx context.Context, events []HealthEvent) error
	ListEvents(ctx context.Context, deviceID string, cursor string, limit int) (events []HealthEvent, nextCursor string, err error)
	GetSummary(ctx context.Context) (Summary, error)
}

type fileStorage struct {
	mu             sync.RWMutex
	dir            string
	snapshotsPath  string
	eventsPath     string
	snapshots      map[string]HealthSnapshot
	events         []HealthEvent
	maxEventsPerDev int
}

type snapshotFileData struct {
	SchemaVersion int              `json:"schema_version"`
	Snapshots     []HealthSnapshot `json:"snapshots"`
}

type eventFileData struct {
	SchemaVersion int           `json:"schema_version"`
	Events        []HealthEvent `json:"events"`
}

// NewFileRepository 创建基于 JSON 文件的健康数据存储仓储。
func NewFileRepository(dataDir string) (Repository, error) {
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, fmt.Errorf("create health dir: %w", err)
	}

	repo := &fileStorage{
		dir:             dataDir,
		snapshotsPath:  filepath.Join(dataDir, "health_snapshots.json"),
		eventsPath:     filepath.Join(dataDir, "health_events.json"),
		snapshots:      make(map[string]HealthSnapshot),
		events:         make([]HealthEvent, 0),
		maxEventsPerDev: 1000,
	}

	// 加载 Snapshots
	if b, err := os.ReadFile(repo.snapshotsPath); err == nil {
		if len(bytes.TrimSpace(b)) > 0 {
			var data snapshotFileData
			if err := json.Unmarshal(b, &data); err != nil {
				return nil, fmt.Errorf("corrupted snapshot file %s: %w", repo.snapshotsPath, err)
			}
			if data.SchemaVersion != 1 {
				return nil, fmt.Errorf("unsupported snapshot schema version: %d", data.SchemaVersion)
			}
			for _, s := range data.Snapshots {
				repo.snapshots[s.DeviceID] = s
			}
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read snapshot file %s: %w", repo.snapshotsPath, err)
	}

	// 加载 Events
	if b, err := os.ReadFile(repo.eventsPath); err == nil {
		if len(bytes.TrimSpace(b)) > 0 {
			var data eventFileData
			if err := json.Unmarshal(b, &data); err != nil {
				return nil, fmt.Errorf("corrupted event file %s: %w", repo.eventsPath, err)
			}
			if data.SchemaVersion != 1 {
				return nil, fmt.Errorf("unsupported event schema version: %d", data.SchemaVersion)
			}
			repo.events = data.Events
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read event file %s: %w", repo.eventsPath, err)
	}

	return repo, nil
}

func (s *fileStorage) SaveSnapshot(ctx context.Context, snapshot HealthSnapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.snapshots[snapshot.DeviceID] = snapshot
	return s.persistSnapshotsLocked()
}

func (s *fileStorage) GetSnapshot(ctx context.Context, deviceID string) (*HealthSnapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	snap, ok := s.snapshots[deviceID]
	if !ok {
		return nil, ErrNotFound
	}
	cp := snap
	return &cp, nil
}

func (s *fileStorage) ListSnapshots(ctx context.Context) ([]HealthSnapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	list := make([]HealthSnapshot, 0, len(s.snapshots))
	for _, snap := range s.snapshots {
		list = append(list, snap)
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].DeviceID < list[j].DeviceID
	})
	return list, nil
}

func (s *fileStorage) AppendEvents(ctx context.Context, events []HealthEvent) error {
	if len(events) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	s.events = append(s.events, events...)
	// 按时间倒序截断保留
	if len(s.events) > 5000 {
		s.events = s.events[len(s.events)-5000:]
	}
	return s.persistEventsLocked()
}

func (s *fileStorage) ListEvents(ctx context.Context, deviceID string, cursor string, limit int) ([]HealthEvent, string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if limit <= 0 || limit > 200 {
		limit = 50
	}

	var filtered []HealthEvent
	for i := len(s.events) - 1; i >= 0; i-- {
		ev := s.events[i]
		if deviceID != "" && ev.DeviceID != deviceID {
			continue
		}
		filtered = append(filtered, ev)
	}

	startIndex := 0
	if cursor != "" {
		if idx, err := strconv.Atoi(cursor); err == nil && idx >= 0 && idx < len(filtered) {
			startIndex = idx
		}
	}

	endIndex := startIndex + limit
	if endIndex > len(filtered) {
		endIndex = len(filtered)
	}

	page := filtered[startIndex:endIndex]
	nextCursor := ""
	if endIndex < len(filtered) {
		nextCursor = strconv.Itoa(endIndex)
	}

	return page, nextCursor, nil
}

func (s *fileStorage) GetSummary(ctx context.Context) (Summary, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	sum := Summary{
		Total: len(s.snapshots),
	}
	for _, snap := range s.snapshots {
		switch snap.Status {
		case StatusHealthy:
			sum.Healthy++
		case StatusDegraded:
			sum.Degraded++
			sum.UnhealthyCount++
		case StatusOffline:
			sum.Offline++
			sum.UnhealthyCount++
		case StatusUnknown:
			sum.Unknown++
		}
	}
	return sum, nil
}

func (s *fileStorage) persistSnapshotsLocked() error {
	list := make([]HealthSnapshot, 0, len(s.snapshots))
	for _, snap := range s.snapshots {
		list = append(list, snap)
	}
	data := snapshotFileData{
		SchemaVersion: 1,
		Snapshots:     list,
	}
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.snapshotsPath + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, s.snapshotsPath)
}

func (s *fileStorage) persistEventsLocked() error {
	data := eventFileData{
		SchemaVersion: 1,
		Events:        s.events,
	}
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.eventsPath + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, s.eventsPath)
}
