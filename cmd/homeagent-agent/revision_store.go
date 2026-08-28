package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"homeagent/internal/daemon"
)

const (
	revisionSchemaVersion        = 1
	reportTypeDeviceNetworkState = "device-network-state"
	reportTypeRouterPrefixes     = "router-prefixes"
)

var (
	errUnknownRevisionSchema = errors.New("unknown network report revision schema")
	errRevisionExhausted     = errors.New("network report revision exhausted")
)

type revisionKey struct {
	ReportType string
	DeviceID   string
	NetworkID  string
}

type revisionRecord struct {
	SchemaVersion int    `json:"schema_version"`
	ReportType    string `json:"report_type"`
	DeviceID      string `json:"device_id"`
	NetworkID     string `json:"network_id"`
	Revision      uint64 `json:"revision"`
}

type revisionStore struct {
	root  string
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func newRevisionStore(deviceConfigDir string) *revisionStore {
	return &revisionStore{root: filepath.Join(deviceConfigDir, "network-report-revisions"), locks: make(map[string]*sync.Mutex)}
}

func encodeRevisionPathPart(value string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(value))
}

func (s *revisionStore) pathFor(key revisionKey) string {
	return filepath.Join(s.root, encodeRevisionPathPart(key.ReportType), encodeRevisionPathPart(key.DeviceID), encodeRevisionPathPart(key.NetworkID)+".json")
}

func (s *revisionStore) lockFor(path string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	if lock := s.locks[path]; lock != nil {
		return lock
	}
	lock := &sync.Mutex{}
	s.locks[path] = lock
	return lock
}

func (s *revisionStore) Current(key revisionKey) (uint64, error) {
	path := s.pathFor(key)
	lock := s.lockFor(path)
	lock.Lock()
	defer lock.Unlock()
	record, err := s.read(path, key)
	if err != nil {
		return 0, err
	}
	return record.Revision, nil
}

func (s *revisionStore) Allocate(key revisionKey) (uint64, error) {
	path := s.pathFor(key)
	lock := s.lockFor(path)
	lock.Lock()
	defer lock.Unlock()
	record, err := s.read(path, key)
	if errors.Is(err, os.ErrNotExist) {
		record = revisionRecord{SchemaVersion: revisionSchemaVersion, ReportType: key.ReportType, DeviceID: key.DeviceID, NetworkID: key.NetworkID}
	} else if err != nil {
		if errors.Is(err, errUnknownRevisionSchema) {
			return 0, err
		}
		if quarantineErr := s.quarantine(path); quarantineErr != nil {
			return 0, fmt.Errorf("quarantine revision state: %w", quarantineErr)
		}
		record = revisionRecord{SchemaVersion: revisionSchemaVersion, ReportType: key.ReportType, DeviceID: key.DeviceID, NetworkID: key.NetworkID}
	}
	if record.Revision == math.MaxUint64 {
		return 0, errRevisionExhausted
	}
	record.Revision++
	if err := s.write(path, record); err != nil {
		return 0, err
	}
	return record.Revision, nil
}

func (s *revisionStore) AdvanceAfterConflict(key revisionKey, current uint64) (uint64, error) {
	path := s.pathFor(key)
	lock := s.lockFor(path)
	lock.Lock()
	defer lock.Unlock()
	record, err := s.read(path, key)
	if err != nil {
		return 0, err
	}
	base := record.Revision
	if current > base {
		base = current
	}
	if base == math.MaxUint64 {
		return 0, errRevisionExhausted
	}
	record.Revision = base + 1
	if err := s.write(path, record); err != nil {
		return 0, err
	}
	return record.Revision, nil
}

func (s *revisionStore) read(path string, key revisionKey) (revisionRecord, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return revisionRecord{}, err
	}
	var record revisionRecord
	if err := json.Unmarshal(b, &record); err != nil {
		return revisionRecord{}, fmt.Errorf("decode revision state: %w", err)
	}
	if record.SchemaVersion != revisionSchemaVersion {
		return revisionRecord{}, fmt.Errorf("%w: %d", errUnknownRevisionSchema, record.SchemaVersion)
	}
	if record.ReportType != key.ReportType || record.DeviceID != key.DeviceID || record.NetworkID != key.NetworkID || record.Revision == 0 {
		return revisionRecord{}, errors.New("invalid network report revision state")
	}
	return record, nil
}

func (s *revisionStore) quarantine(path string) error {
	suffix := fmt.Sprintf(".corrupt-%s-%d", time.Now().UTC().Format("20060102T150405.000000000Z"), time.Now().UnixNano())
	return os.Rename(path, path+suffix)
}

func (s *revisionStore) write(path string, record revisionRecord) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(s.root, 0700); err != nil {
			return err
		}
	}
	b, err := json.Marshal(record)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if err := daemon.AtomicWrite(path, b); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		dir, err := os.Open(filepath.Dir(path))
		if err != nil {
			return err
		}
		err = dir.Sync()
		closeErr := dir.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}
