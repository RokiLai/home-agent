package routermonitor

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Report struct {
	Raw    string `json:"raw,omitempty"`
	Failed bool   `json:"failed,omitempty"`
}

type View struct {
	State       string    `json:"state"`
	Received    time.Time `json:"received"`
	LastSuccess time.Time `json:"last_success"`
	Snapshot    *Snapshot `json:"snapshot,omitempty"`
}

type Latest struct {
	mu   sync.Mutex
	path string
}

func NewLatest(path string) *Latest { return &Latest{path: path} }

func (s *Latest) read() (View, error) {
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return View{State: "waiting"}, nil
	}
	if err != nil {
		return View{}, err
	}
	var v View
	err = json.Unmarshal(raw, &v)
	return v, err
}

func (s *Latest) Read(now time.Time) (View, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, err := s.read()
	if !v.Received.IsZero() && now.Sub(v.Received) > 180*time.Second {
		v.State = "stale"
	}
	return v, err
}

func (s *Latest) Save(report Report, now time.Time) error {
	var snapshot Snapshot
	if !report.Failed {
		var err error
		snapshot, err = ParseSnapshot(strings.NewReader(report.Raw))
		if err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, err := s.read()
	if err != nil {
		return err
	}
	if !now.After(v.Received) {
		return nil
	}
	v.Received = now
	if report.Failed {
		v.State = "collection_failed"
	} else {
		v.State = snapshot.Mode
		v.Snapshot = &snapshot
		v.LastSuccess = now
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(s.path), ".router-monitor-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(raw)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), s.path)
}
