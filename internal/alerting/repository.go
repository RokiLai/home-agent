// Package alerting 提供告警记录、静默规则与投递日志的持久化存储。
package alerting

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

// ErrNotFound 当查询的告警或静默规则不存在时返回此错误。
var ErrNotFound = errors.New("alerting record not found")

// AlertFilter 封装告警查询的过滤条件。
type AlertFilter struct {
	DeviceID string
	State    string
	Severity string
	Cursor   string
	Limit    int
}

// DeliveryFilter 封装投递记录查询的过滤条件。
type DeliveryFilter struct {
	AlertID   string
	ChannelID string
	Cursor    string
	Limit     int
}

// Repository 定义告警存储契约。
type Repository interface {
	SaveAlert(ctx context.Context, alert Alert) error
	GetAlert(ctx context.Context, id string) (*Alert, error)
	FindActiveAlertByFingerprint(ctx context.Context, fingerprint string) (*Alert, error)
	ListAlerts(ctx context.Context, filter AlertFilter) (alerts []Alert, nextCursor string, err error)
	SaveSilence(ctx context.Context, silence Silence) error
	DeleteSilence(ctx context.Context, id string) error
	ListSilences(ctx context.Context) ([]Silence, error)
	RecordDeliveryAttempt(ctx context.Context, attempt DeliveryAttempt) error
	ListDeliveryAttempts(ctx context.Context, filter DeliveryFilter) (attempts []DeliveryAttempt, nextCursor string, err error)
	ListPendingRetries(ctx context.Context) ([]DeliveryAttempt, error)
}

type fileStorage struct {
	mu             sync.RWMutex
	alertsPath     string
	silencesPath   string
	deliveriesPath string
	alerts         map[string]Alert
	silences       map[string]Silence
	deliveries     []DeliveryAttempt
}

type alertsFileData struct {
	SchemaVersion int     `json:"schema_version"`
	Alerts        []Alert `json:"alerts"`
}

type silencesFileData struct {
	SchemaVersion int       `json:"schema_version"`
	Silences      []Silence `json:"silences"`
}

type deliveriesFileData struct {
	SchemaVersion int               `json:"schema_version"`
	Deliveries    []DeliveryAttempt `json:"deliveries"`
}

// NewFileRepository 创建基于 JSON 文件的告警仓储实例。
func NewFileRepository(dataDir string) (Repository, error) {
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, fmt.Errorf("create alerting dir: %w", err)
	}

	repo := &fileStorage{
		alertsPath:     filepath.Join(dataDir, "alerts.json"),
		silencesPath:   filepath.Join(dataDir, "silences.json"),
		deliveriesPath: filepath.Join(dataDir, "deliveries.json"),
		alerts:         make(map[string]Alert),
		silences:       make(map[string]Silence),
		deliveries:     make([]DeliveryAttempt, 0),
	}

	// 加载 alerts
	if b, err := os.ReadFile(repo.alertsPath); err == nil {
		if len(bytes.TrimSpace(b)) > 0 {
			var data alertsFileData
			if err := json.Unmarshal(b, &data); err != nil {
				return nil, fmt.Errorf("corrupted alerts file %s: %w", repo.alertsPath, err)
			}
			if data.SchemaVersion != 1 {
				return nil, fmt.Errorf("unsupported alerts schema version: %d", data.SchemaVersion)
			}
			for _, a := range data.Alerts {
				repo.alerts[a.ID] = a
			}
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read alerts file %s: %w", repo.alertsPath, err)
	}

	// 加载 silences
	if b, err := os.ReadFile(repo.silencesPath); err == nil {
		if len(bytes.TrimSpace(b)) > 0 {
			var data silencesFileData
			if err := json.Unmarshal(b, &data); err != nil {
				return nil, fmt.Errorf("corrupted silences file %s: %w", repo.silencesPath, err)
			}
			if data.SchemaVersion != 1 {
				return nil, fmt.Errorf("unsupported silences schema version: %d", data.SchemaVersion)
			}
			for _, s := range data.Silences {
				repo.silences[s.ID] = s
			}
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read silences file %s: %w", repo.silencesPath, err)
	}

	// 加载 deliveries
	if b, err := os.ReadFile(repo.deliveriesPath); err == nil {
		if len(bytes.TrimSpace(b)) > 0 {
			var data deliveriesFileData
			if err := json.Unmarshal(b, &data); err != nil {
				return nil, fmt.Errorf("corrupted deliveries file %s: %w", repo.deliveriesPath, err)
			}
			if data.SchemaVersion != 1 {
				return nil, fmt.Errorf("unsupported deliveries schema version: %d", data.SchemaVersion)
			}
			repo.deliveries = data.Deliveries
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read deliveries file %s: %w", repo.deliveriesPath, err)
	}

	return repo, nil
}

func (s *fileStorage) SaveAlert(ctx context.Context, alert Alert) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.alerts[alert.ID] = alert
	return s.persistAlertsLocked()
}

func (s *fileStorage) GetAlert(ctx context.Context, id string) (*Alert, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	a, ok := s.alerts[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := a
	return &cp, nil
}

func (s *fileStorage) FindActiveAlertByFingerprint(ctx context.Context, fingerprint string) (*Alert, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, a := range s.alerts {
		if a.Fingerprint == fingerprint && a.State == AlertFiring {
			cp := a
			return &cp, nil
		}
	}
	return nil, nil
}

func (s *fileStorage) ListAlerts(ctx context.Context, filter AlertFilter) ([]Alert, string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if filter.Limit <= 0 || filter.Limit > 200 {
		filter.Limit = 50
	}

	var filtered []Alert
	for _, a := range s.alerts {
		if filter.DeviceID != "" && a.DeviceID != filter.DeviceID {
			continue
		}
		if filter.State != "" && string(a.State) != filter.State {
			continue
		}
		if filter.Severity != "" && string(a.Severity) != filter.Severity {
			continue
		}
		filtered = append(filtered, a)
	}

	// 倒序排列（新发生/活跃的在前）
	sort.Slice(filtered, func(i, j int) bool {
		if filtered[i].State != filtered[j].State {
			return filtered[i].State == AlertFiring // firing 排在 resolved 前面
		}
		return filtered[i].OpenedAt.After(filtered[j].OpenedAt)
	})

	startIndex := 0
	if filter.Cursor != "" {
		if idx, err := strconv.Atoi(filter.Cursor); err == nil && idx >= 0 && idx < len(filtered) {
			startIndex = idx
		}
	}

	endIndex := startIndex + filter.Limit
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

func (s *fileStorage) SaveSilence(ctx context.Context, silence Silence) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.silences[silence.ID] = silence
	return s.persistSilencesLocked()
}

func (s *fileStorage) DeleteSilence(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.silences[id]; !ok {
		return ErrNotFound
	}
	delete(s.silences, id)
	return s.persistSilencesLocked()
}

func (s *fileStorage) ListSilences(ctx context.Context) ([]Silence, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	list := make([]Silence, 0, len(s.silences))
	for _, sil := range s.silences {
		list = append(list, sil)
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].CreatedAt.After(list[j].CreatedAt)
	})
	return list, nil
}

func (s *fileStorage) RecordDeliveryAttempt(ctx context.Context, attempt DeliveryAttempt) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.deliveries = append(s.deliveries, attempt)
	if len(s.deliveries) > 3000 {
		s.deliveries = s.deliveries[len(s.deliveries)-3000:]
	}
	return s.persistDeliveriesLocked()
}

func (s *fileStorage) ListDeliveryAttempts(ctx context.Context, filter DeliveryFilter) ([]DeliveryAttempt, string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if filter.Limit <= 0 || filter.Limit > 200 {
		filter.Limit = 50
	}

	var filtered []DeliveryAttempt
	for i := len(s.deliveries) - 1; i >= 0; i-- {
		d := s.deliveries[i]
		if filter.AlertID != "" && d.AlertID != filter.AlertID {
			continue
		}
		if filter.ChannelID != "" && d.ChannelID != filter.ChannelID {
			continue
		}
		filtered = append(filtered, d)
	}

	startIndex := 0
	if filter.Cursor != "" {
		if idx, err := strconv.Atoi(filter.Cursor); err == nil && idx >= 0 && idx < len(filtered) {
			startIndex = idx
		}
	}

	endIndex := startIndex + filter.Limit
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

func (s *fileStorage) ListPendingRetries(ctx context.Context) ([]DeliveryAttempt, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// 查找每个 (AlertID, ChannelID, Event, DeliveryID) 组合最新的未完成重试记录
	latestMap := make(map[string]DeliveryAttempt)
	for _, d := range s.deliveries {
		key := fmt.Sprintf("%s:%s:%s:%s", d.AlertID, d.ChannelID, d.Event, d.DeliveryID)
		if cur, exists := latestMap[key]; !exists || d.AttemptNumber > cur.AttemptNumber {
			latestMap[key] = d
		}
	}

	var pending []DeliveryAttempt
	for _, d := range latestMap {
		if !d.Delivered && d.NextRetryAt != nil && d.AttemptNumber < 6 {
			pending = append(pending, d)
		}
	}

	return pending, nil
}

func (s *fileStorage) persistAlertsLocked() error {
	list := make([]Alert, 0, len(s.alerts))
	for _, a := range s.alerts {
		list = append(list, a)
	}
	data := alertsFileData{
		SchemaVersion: 1,
		Alerts:        list,
	}
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.alertsPath + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, s.alertsPath)
}

func (s *fileStorage) persistSilencesLocked() error {
	list := make([]Silence, 0, len(s.silences))
	for _, sil := range s.silences {
		list = append(list, sil)
	}
	data := silencesFileData{
		SchemaVersion: 1,
		Silences:      list,
	}
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.silencesPath + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, s.silencesPath)
}

func (s *fileStorage) persistDeliveriesLocked() error {
	data := deliveriesFileData{
		SchemaVersion: 1,
		Deliveries:    s.deliveries,
	}
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.deliveriesPath + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, s.deliveriesPath)
}

