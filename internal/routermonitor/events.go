package routermonitor

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	ErrInvalidEventFormat = errors.New("invalid event format")
	ErrGapDetected        = errors.New("event gap detected")
)

type Event struct {
	Version       int       `json:"version"`
	BootID        string    `json:"boot_id"`
	Generation    string    `json:"generation"`
	Seq           int64     `json:"seq"`
	Cycle         int64     `json:"cycle"`
	Component     string    `json:"component"`
	EventType     string    `json:"event_type"` // observed, action_requested, action_success, action_failed, action_restricted, recovered, unhealthy_recheck
	Reason        string    `json:"reason"`
	ActionID      string    `json:"action_id,omitempty"`
	RecoveryGroup string    `json:"recovery_group,omitempty"`
	Uptime        int64     `json:"uptime"`
	CreatedAt     time.Time `json:"created_at,omitempty"`
}

type EventBatch struct {
	BootID      string  `json:"boot_id"`
	Generation  string  `json:"generation"`
	EarliestSeq int64   `json:"earliest_seq"`
	LatestSeq   int64   `json:"latest_seq"`
	Events      []Event `json:"events"`
}

// ParseEvents parses router-guard.sh events output.
func ParseEvents(r io.Reader) (EventBatch, error) {
	scanner := bufio.NewScanner(r)
	if !scanner.Scan() {
		return EventBatch{}, ErrInvalidEventFormat
	}
	header := scanner.Text()
	fields := strings.Fields(header)
	if len(fields) < 3 {
		return EventBatch{}, ErrInvalidEventFormat
	}
	var batch EventBatch
	for _, f := range fields {
		k, v, ok := strings.Cut(f, "=")
		if !ok {
			return EventBatch{}, ErrInvalidEventFormat
		}
		switch k {
		case "generation":
			batch.Generation = v
		case "earliest_seq":
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n < 0 {
				return EventBatch{}, ErrInvalidEventFormat
			}
			batch.EarliestSeq = n
		case "latest_seq":
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n < 0 {
				return EventBatch{}, ErrInvalidEventFormat
			}
			batch.LatestSeq = n
		case "boot_id":
			batch.BootID = v
		}
	}
	if batch.Generation == "" {
		return EventBatch{}, ErrInvalidEventFormat
	}

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 11 {
			return EventBatch{}, ErrInvalidEventFormat
		}
		ver, err := strconv.Atoi(parts[0])
		if err != nil {
			return EventBatch{}, ErrInvalidEventFormat
		}
		seq, err := strconv.ParseInt(parts[3], 10, 64)
		if err != nil || seq <= 0 {
			return EventBatch{}, ErrInvalidEventFormat
		}
		cycle, err := strconv.ParseInt(parts[4], 10, 64)
		if err != nil || cycle < 0 {
			return EventBatch{}, ErrInvalidEventFormat
		}
		uptime, err := strconv.ParseInt(parts[10], 10, 64)
		if err != nil || uptime < 0 {
			return EventBatch{}, ErrInvalidEventFormat
		}
		bootID := parts[1]
		if batch.BootID == "" && bootID != "" {
			batch.BootID = bootID
		}
		batch.Events = append(batch.Events, Event{
			Version:       ver,
			BootID:        bootID,
			Generation:    parts[2],
			Seq:           seq,
			Cycle:         cycle,
			Component:     parts[5],
			EventType:     parts[6],
			Reason:        parts[7],
			ActionID:      parts[8],
			RecoveryGroup: parts[9],
			Uptime:        uptime,
		})
	}
	if err := scanner.Err(); err != nil {
		return EventBatch{}, err
	}
	return batch, nil
}

type TimelineItem struct {
	Event
	DisplayStatus string `json:"display_status"`
	Description   string `json:"description"`
}

// BuildTimelineItems projects events into user-facing timeline items based on contract.
func BuildTimelineItems(events []Event) []TimelineItem {
	items := make([]TimelineItem, 0, len(events))
	pendingActions := make(map[string]string)

	for _, ev := range events {
		item := TimelineItem{Event: ev}
		switch ev.EventType {
		case "observed":
			item.DisplayStatus = "检测到异常，无恢复动作"
			item.Description = "探测异常未达阈值或处于观察模式"
		case "action_restricted":
			item.DisplayStatus = "受限"
			item.Description = fmt.Sprintf("恢复受限（%s），不生成执行结果", ev.Reason)
		case "action_blocked":
			item.DisplayStatus = "恢复受阻"
			item.Description = fmt.Sprintf("前置条件拒绝（%s），命令未执行", ev.Reason)
		case "action_requested":
			item.DisplayStatus = "已请求"
			item.Description = "已通过检查并请求执行恢复命令，结果待定"
			if ev.ActionID != "" {
				pendingActions[ev.ActionID] = ev.Component
			}
		case "action_success":
			item.DisplayStatus = "待复查"
			item.Description = "恢复命令返回成功，等待下一轮健康复查"
		case "action_failed":
			item.DisplayStatus = "执行失败或超时"
			item.Description = fmt.Sprintf("恢复命令执行失败或超时（%s）", ev.Reason)
			if ev.ActionID != "" {
				delete(pendingActions, ev.ActionID)
			}
		case "recovered":
			if ev.ActionID != "" && pendingActions[ev.ActionID] == ev.Component {
				item.DisplayStatus = "已恢复"
				item.Description = "后续轮次对同一组件复查健康，恢复成功"
				delete(pendingActions, ev.ActionID)
			} else {
				item.DisplayStatus = "待复查或结果未知"
				item.Description = "健康事件未匹配到有效的待复查动作"
			}
		case "unhealthy_recheck":
			item.DisplayStatus = "仍异常"
			item.Description = "恢复执行后复查仍处于异常状态"
			if ev.ActionID != "" {
				delete(pendingActions, ev.ActionID)
			}
		case "gap":
			item.DisplayStatus = "历史缺口"
			item.Description = "由于重启或缓冲覆盖，部分事件未能补采"
		default:
			item.DisplayStatus = "未知"
			item.Description = "未知事件类型"
		}
		items = append(items, item)
	}
	return items
}

type EventStoreState struct {
	LastBootID     string  `json:"last_boot_id"`
	LastGeneration string  `json:"last_generation"`
	AckCursor      int64   `json:"ack_cursor"`
	Events         []Event `json:"events"`
}

type EventStore struct {
	mu       sync.Mutex
	path     string
	devID    string
	routerID string
}

func NewEventStore(dataDir, devID, routerID string) *EventStore {
	binding := sha256.Sum256([]byte(devID + "\x00" + routerID))
	return &EventStore{
		path:     filepath.Join(dataDir, fmt.Sprintf("router-events-%x.json", binding)),
		devID:    devID,
		routerID: routerID,
	}
}

func (s *EventStore) load() (EventStoreState, error) {
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return EventStoreState{Events: []Event{}}, nil
	}
	if err != nil {
		return EventStoreState{}, err
	}
	var state EventStoreState
	err = json.Unmarshal(raw, &state)
	return state, err
}

func (s *EventStore) SaveEvents(batch EventBatch, now time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, err := s.load()
	if err != nil {
		return 0, err
	}

	generationChanged := state.LastGeneration != "" && state.LastGeneration != batch.Generation
	bootChanged := state.LastBootID != "" && state.LastBootID != batch.BootID

	if bootChanged || generationChanged {
		state.LastBootID = batch.BootID
		state.LastGeneration = batch.Generation
		state.AckCursor = 0
	} else if state.LastGeneration == "" {
		state.LastBootID = batch.BootID
		state.LastGeneration = batch.Generation
	}

	if len(batch.Events) == 0 {
		return state.AckCursor, nil
	}

	firstSeq := batch.Events[0].Seq
	lastSeq := batch.Events[len(batch.Events)-1].Seq

	if lastSeq <= state.AckCursor {
		return state.AckCursor, nil
	}

	// 检查跳号：若批次事件首个序号大于期望游标 + 1
	if firstSeq > state.AckCursor+1 {
		// 只有源端声明了 EarliestSeq > state.AckCursor+1（即确实因满缓冲导致旧事件丢失）才记录合法 gap
		if batch.EarliestSeq > state.AckCursor+1 {
			gapEvent := Event{
				Version:    1,
				BootID:     batch.BootID,
				Generation: batch.Generation,
				Seq:        state.AckCursor + 1,
				EventType:  "gap",
				Reason:     "buffer_overflow_gap",
				CreatedAt:  now,
			}
			state.Events = append(state.Events, gapEvent)
		} else {
			return 0, ErrGapDetected
		}
	}

	for _, ev := range batch.Events {
		if ev.Seq <= state.AckCursor {
			continue
		}
		ev.CreatedAt = now
		state.Events = append(state.Events, ev)
		state.AckCursor = ev.Seq
	}

	cutoff := now.Add(-30 * 24 * time.Hour)
	kept := make([]Event, 0, len(state.Events))
	for _, ev := range state.Events {
		if !ev.CreatedAt.IsZero() && ev.CreatedAt.Before(cutoff) {
			continue
		}
		kept = append(kept, ev)
	}
	if len(kept) > 10000 {
		kept = kept[len(kept)-10000:]
	}
	state.Events = kept

	raw, err := json.Marshal(state)
	if err != nil {
		return 0, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".router-events-")
	if err != nil {
		return 0, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return 0, err
	}
	if err := tmp.Close(); err != nil {
		return 0, err
	}
	if err := os.Rename(tmp.Name(), s.path); err != nil {
		return 0, err
	}

	return state.AckCursor, nil
}

func (s *EventStore) QueryEvents(limit int) ([]Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.load()
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > len(state.Events) {
		limit = len(state.Events)
	}
	start := len(state.Events) - limit
	res := make([]Event, limit)
	copy(res, state.Events[start:])
	return res, nil
}

func (s *EventStore) GetAckCursor() (bootID, generation string, cursor int64, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.load()
	if err != nil {
		return "", "", 0, err
	}
	return state.LastBootID, state.LastGeneration, state.AckCursor, nil
}
