package upgradeplan

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"sync"
	"time"

	"homeagent/internal/command"
)

// CreatePlanRequest 包含创建 UpgradePlan 的输入参数。
type CreatePlanRequest struct {
	DeviceID             string
	RequestedBy          command.Actor
	IdempotencyKey       string
	TargetVersion        string
	TargetManifestDigest *string
	BridgeVersion        *string
	BridgeCommandID      *string
	TargetCommandID      *string
	InitialStage         PlanStage
	Snapshot             PlanSnapshot
}

// Service 提供 UpgradePlan 的增删改查与两跳状态机流转服务。
type Service struct {
	mu    sync.RWMutex
	plans map[string]*UpgradePlan
}

// NewService 创建 UpgradePlan 服务实例。
func NewService() *Service {
	return &Service{
		plans: make(map[string]*UpgradePlan),
	}
}

func generatePlanID() string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("plan-%d-%d", time.Now().UnixNano(), time.Now().Nanosecond())))
	return fmt.Sprintf("plan-%s", hex.EncodeToString(sum[:8]))
}

// CreatePlan 幂等创建或获取指定设备的 UpgradePlan。
func (s *Service) CreatePlan(req CreatePlanRequest) (*UpgradePlan, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 1. 检查幂等键
	if req.IdempotencyKey != "" {
		for _, p := range s.plans {
			if p.DeviceID == req.DeviceID && p.IdempotencyKey == req.IdempotencyKey && p.RequestedBy.ID == req.RequestedBy.ID {
				if p.Snapshot.RequestDigest != req.Snapshot.RequestDigest {
					return nil, false, ErrIdempotencyConflict
				}
				return p, false, nil
			}
		}
	}

	// 2. 检查同设备是否已有非终态 Plan
	for _, p := range s.plans {
		if p.DeviceID == req.DeviceID && !p.Stage.Terminal() {
			return nil, false, ErrPlanInProgress
		}
	}

	planID := generatePlanID()
	now := time.Now().UTC()

	initialStage := req.InitialStage
	if initialStage == "" {
		if req.BridgeVersion != nil {
			initialStage = StageBridgePending
		} else {
			initialStage = StageTargetPending
		}
	}

	bridgeCmdID := req.BridgeCommandID
	if bridgeCmdID == nil && req.BridgeVersion != nil {
		id := planID + ":bridge"
		bridgeCmdID = &id
	}

	targetCmdID := req.TargetCommandID
	if targetCmdID == nil {
		id := planID + ":target"
		targetCmdID = &id
	}

	p := &UpgradePlan{
		PlanID:               planID,
		DeviceID:             req.DeviceID,
		RequestedBy:          req.RequestedBy,
		IdempotencyKey:       req.IdempotencyKey,
		TargetVersion:        req.TargetVersion,
		TargetManifestDigest: req.TargetManifestDigest,
		BridgeVersion:        req.BridgeVersion,
		BridgeCommandID:      bridgeCmdID,
		TargetCommandID:      targetCmdID,
		Stage:                initialStage,
		CreatedAt:            now,
		UpdatedAt:            now,
		Revision:             1,
		Snapshot:             req.Snapshot,
	}

	s.plans[planID] = p
	return p, true, nil
}

// GetPlan 查询指定 Plan。
func (s *Service) GetPlan(planID string) (*UpgradePlan, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	p, ok := s.plans[planID]
	if !ok {
		return nil, ErrPlanNotFound
	}
	cp := *p
	return &cp, nil
}

// GetActivePlanByDevice 查询指定设备当前的活动非终态 Plan。
func (s *Service) GetActivePlanByDevice(deviceID string) (*UpgradePlan, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, p := range s.plans {
		if p.DeviceID == deviceID && !p.Stage.Terminal() {
			cp := *p
			return &cp, nil
		}
	}
	return nil, ErrPlanNotFound
}

// ListPlans 依据过滤条件列出 UpgradePlan 列表。
func (s *Service) ListPlans(filter Filter) ([]UpgradePlan, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var out []UpgradePlan
	for _, p := range s.plans {
		if filter.DeviceID != "" && p.DeviceID != filter.DeviceID {
			continue
		}
		if filter.Stage != "" && p.Stage != filter.Stage {
			continue
		}
		out = append(out, *p)
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})

	if filter.Limit > 0 && len(out) > filter.Limit {
		out = out[:filter.Limit]
	}

	return out, nil
}

// TransitionStage 将 Plan 转入下一合法阶段。
func (s *Service) TransitionStage(planID string, expectedRev uint64, newStage PlanStage, failureReason string) (*UpgradePlan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	p, ok := s.plans[planID]
	if !ok {
		return nil, ErrPlanNotFound
	}
	if p.Revision != expectedRev {
		return nil, ErrPlanConflict
	}
	if p.Stage.Terminal() {
		return nil, ErrInvalidPlanTransition
	}

	p.Stage = newStage
	if failureReason != "" {
		p.FailureReason = failureReason
	}
	p.UpdatedAt = time.Now().UTC()
	p.Revision++

	cp := *p
	return &cp, nil
}

// CancelPlan 取消尚未完成的 UpgradePlan。
func (s *Service) CancelPlan(planID string) (*UpgradePlan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	p, ok := s.plans[planID]
	if !ok {
		return nil, ErrPlanNotFound
	}
	if p.Stage.Terminal() {
		return nil, ErrInvalidPlanTransition
	}

	p.Stage = StageCanceled
	p.UpdatedAt = time.Now().UTC()
	p.Revision++

	cp := *p
	return &cp, nil
}
