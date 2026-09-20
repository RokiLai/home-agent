package command

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

type Clock interface{ Now() time.Time }
type realClock struct{}

func (realClock) Now() time.Time { return time.Now().UTC() }

type Service struct {
	repo  Repository
	clock Clock
}

func NewService(repo Repository, clock Clock) *Service {
	if clock == nil {
		clock = realClock{}
	}
	return &Service{repo: repo, clock: clock}
}
func (s *Service) Create(req CreateRequest) (Command, bool, error) { return s.repo.CreateOrGet(req) }
func (s *Service) Get(id ID) (Command, error)                      { return s.repo.Get(id) }
func (s *Service) List(f Filter) (Page, error)                     { return s.repo.List(f) }

// QueuedForDevice 返回指定设备仍可恢复的任务，调用方必须再次校验 TTL 和设备连接。
func (s *Service) QueuedForDevice(deviceID string, limit int) ([]Command, error) {
	p, err := s.repo.List(Filter{DeviceID: deviceID, Status: StatusQueued, Limit: limit})
	if err != nil {
		return nil, err
	}
	return p.Commands, nil
}
func (s *Service) StartDispatch(id ID) (Command, error) {
	c, e := s.repo.Get(id)
	if e != nil {
		return Command{}, e
	}
	b := make([]byte, 12)
	if _, e = rand.Read(b); e != nil {
		return Command{}, e
	}
	return s.repo.Transition(id, c.Revision, Transition{Status: StatusDispatching, AttemptID: hex.EncodeToString(b), At: s.clock.Now()})
}
func (s *Service) DispatchResult(id ID, delivered bool) (Command, error) {
	c, e := s.repo.Get(id)
	if e != nil {
		return Command{}, e
	}
	if c.Status != StatusDispatching {
		return c, nil
	}
	t := Transition{Status: StatusDispatched, At: s.clock.Now()}
	if !delivered {
		t.Status = StatusFailed
		t.ErrorCode = "delivery_unavailable"
		t.ErrorMessage = "no subscriber accepted delivery"
	}
	return s.repo.Transition(id, c.Revision, t)
}

// Requeue 将暂时没有在线订阅者的投递重新放回 queued。
func (s *Service) Requeue(id ID) (Command, error) {
	c, err := s.repo.Get(id)
	if err != nil {
		return Command{}, err
	}
	if c.Status == StatusQueued {
		return c, nil
	}
	if c.Status != StatusDispatching {
		return Command{}, ErrInvalidTransition
	}
	return s.repo.Transition(id, c.Revision, Transition{Status: StatusQueued, At: s.clock.Now()})
}
func (s *Service) MarkLegacy(id ID) (Command, error) {
	c, e := s.repo.Get(id)
	if e != nil {
		return Command{}, e
	}
	if c.Status != StatusDispatched {
		return c, nil
	}
	return s.repo.Transition(id, c.Revision, Transition{Status: StatusLegacyUntracked, At: s.clock.Now()})
}
func (s *Service) MarkProjection(id ID, status, errorMessage string) (Command, error) {
	c, e := s.repo.Get(id)
	if e != nil {
		return Command{}, e
	}
	return s.repo.UpdateProjection(id, c.Revision, ProjectionState{Status: status, Error: errorMessage})
}
func (s *Service) UpdateProgress(id ID, progress UpgradeProgress) (Command, error) {
	c, e := s.repo.Get(id)
	if e != nil {
		return Command{}, e
	}
	return s.repo.UpdateProgress(id, c.Revision, progress)
}
func (s *Service) ProjectionPending(limit int) ([]Command, error) {
	return s.repo.ListProjectionPending(limit)
}
func (s *Service) Accept(id ID) (Command, error) {
	c, e := s.repo.Get(id)
	if e != nil {
		return Command{}, e
	}
	if c.Status == StatusAccepted {
		return c, nil
	}
	return s.repo.Transition(id, c.Revision, Transition{Status: StatusAccepted, At: s.clock.Now()})
}
func (s *Service) Finish(id ID, status Status, result json.RawMessage, errorCode, errorMessage string) (Command, error) {
	c, e := s.repo.Get(id)
	if e != nil {
		return Command{}, e
	}
	canonical := struct {
		Status       Status          `json:"status"`
		Result       json.RawMessage `json:"result"`
		ErrorCode    string          `json:"error_code"`
		ErrorMessage string          `json:"error_message"`
	}{status, result, errorCode, errorMessage}
	b, _ := json.Marshal(canonical)
	h := sha256.Sum256(b)
	d := hex.EncodeToString(h[:])
	if c.Terminal() {
		if c.FinalAckDigest == d {
			return c, nil
		}
		if c.Status == StatusTimedOut || c.Status == StatusCanceled || c.Status == StatusInterrupted {
			updated, err := s.repo.AppendLateAck(id, c.Revision, LateAck{Digest: d, ReceivedAt: s.clock.Now()})
			if err != nil {
				return Command{}, err
			}
			return updated, ErrLateAckAccepted
		}
		return Command{}, ErrInvalidTransition
	}
	return s.repo.Transition(id, c.Revision, Transition{Status: status, Result: result, ErrorCode: errorCode, ErrorMessage: errorMessage, FinalAckDigest: d, ProjectionInputDigest: d, At: s.clock.Now()})
}
func (s *Service) Cancel(id ID) (Command, error) {
	c, e := s.repo.Get(id)
	if e != nil {
		return Command{}, e
	}
	if c.Status == StatusAccepted {
		return Command{}, ErrInvalidTransition
	}
	return s.repo.Transition(id, c.Revision, Transition{Status: StatusCanceled, At: s.clock.Now()})
}
func (s *Service) InterruptNonTerminal() (int, error) {
	return s.repo.InterruptNonTerminal(s.clock.Now())
}

func (s *Service) Expire(limit int) (int, error) {
	items, err := s.repo.ListExpired(s.clock.Now(), limit)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, c := range items {
		if _, err = s.repo.Transition(c.ID, c.Revision, Transition{Status: StatusTimedOut, ErrorCode: "command_timeout", ErrorMessage: "command deadline exceeded", At: s.clock.Now()}); err == nil {
			n++
		} else if !errors.Is(err, ErrConflict) {
			return n, err
		}
	}
	return n, nil
}
