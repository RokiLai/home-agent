// Package alerting 提供告警状态管理、去重、静默判定与多通道投递编排。
package alerting

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"strings"
	"sync"
	"time"

	"homeagent/internal/health"
)

// DeviceNameResolver 用于根据 deviceID 解析设备展示名称。
type DeviceNameResolver interface {
	GetDeviceName(ctx context.Context, deviceID string) string
}

type activeRetryInfo struct {
	cancel context.CancelFunc
	event  string
}

// ServiceConfig 封装创建告警服务所需的依赖。
type ServiceConfig struct {
	Repo             Repository
	NameResolver     DeviceNameResolver
	BackoffSchedule  func(attemptNum int, retryAfter time.Duration) time.Duration
}

// Service 协调告警生命周期与通道分发。
type Service struct {
	mu              sync.RWMutex
	repo            Repository
	nameResolver    DeviceNameResolver
	channels        map[string]Channel
	backoffSchedule func(attemptNum int, retryAfter time.Duration) time.Duration
	activeRetries   map[string]activeRetryInfo // 复合键: alertID:channelID
}

// NewService 创建告警服务实例。
func NewService(cfg ServiceConfig) *Service {
	schedule := cfg.BackoffSchedule
	if schedule == nil {
		schedule = defaultBackoffSchedule
	}
	return &Service{
		repo:            cfg.Repo,
		nameResolver:    cfg.NameResolver,
		channels:        make(map[string]Channel),
		backoffSchedule: schedule,
		activeRetries:   make(map[string]activeRetryInfo),
	}
}

// defaultBackoffSchedule 实现 30s, 2m, 10m, 30m, 2h 标准退避阶梯并叠加 ±20% Jitter。
func defaultBackoffSchedule(attemptNum int, retryAfter time.Duration) time.Duration {
	var base time.Duration
	switch attemptNum {
	case 1:
		base = 30 * time.Second
	case 2:
		base = 2 * time.Minute
	case 3:
		base = 10 * time.Minute
	case 4:
		base = 30 * time.Minute
	default:
		base = 2 * time.Hour
	}
	if retryAfter > 0 && retryAfter > base {
		base = retryAfter
	}
	if base > 2*time.Hour {
		base = 2 * time.Hour
	}
	jitterFactor := 0.8 + 0.4*rand.Float64()
	return time.Duration(float64(base) * jitterFactor)
}

// RegisterChannel 注册一个告警投递通道。
func (s *Service) RegisterChannel(ch Channel) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.channels[ch.ID()] = ch
}

// HandleHealthEvents 消费来自 health 模块的状态变更事件。
func (s *Service) HandleHealthEvents(ctx context.Context, events []health.HealthEvent) {
	for _, ev := range events {
		if err := s.handleSingleEvent(ctx, ev); err != nil {
			slog.Error("failed to handle health event in alerting service", "event_id", ev.ID, "error", err)
		}
	}
}

func (s *Service) handleSingleEvent(ctx context.Context, ev health.HealthEvent) error {
	fp := ComputeFingerprint(ev.DeviceID, ev.ReasonCode)

	switch ev.Type {
	case "opened", "changed":
		active, err := s.repo.FindActiveAlertByFingerprint(ctx, fp)
		if err != nil {
			return err
		}

		var alert Alert
		if active != nil {
			alert = *active
			alert.LastObservedAt = ev.OccurredAt
			alert.Severity = ev.Severity
			alert.Evidence = ev.Evidence
			alert.NotificationRevision++
		} else {
			alert = Alert{
				ID:                   fmt.Sprintf("alt_%s_%s_%d", ev.DeviceID, ev.ReasonCode, ev.OccurredAt.UnixNano()),
				Fingerprint:          fp,
				DeviceID:             ev.DeviceID,
				ReasonCode:           ev.ReasonCode,
				Severity:             ev.Severity,
				State:                AlertFiring,
				OpenedAt:             ev.OccurredAt,
				LastObservedAt:       ev.OccurredAt,
				Summary:              summarizeReason(ev.ReasonCode, ev.Evidence),
				SuggestedAction:      suggestAction(ev.ReasonCode),
				Evidence:             ev.Evidence,
				FiringNotified:       false,
				NotificationRevision: 1,
			}
		}

		// 1. 先持久化 Alert 状态，确保状态落盘
		if err := s.repo.SaveAlert(ctx, alert); err != nil {
			return err
		}

		// 2. 检查静默与是否已通知
		silenced, _ := s.isSilenced(ctx, ev.DeviceID, ev.ReasonCode, ev.OccurredAt)
		if !silenced && !alert.FiringNotified {
			notif := s.buildNotification("alert.firing", alert, "")
			deliveredAny := s.dispatchNotification(ctx, notif, alert.ID)
			if deliveredAny {
				alert.FiringNotified = true
				_ = s.repo.SaveAlert(ctx, alert)
			}
		}

		return nil

	case "resolved":
		active, err := s.repo.FindActiveAlertByFingerprint(ctx, fp)
		if err != nil || active == nil {
			return nil
		}

		alert := *active
		alert.State = AlertResolved
		resolvedAt := ev.OccurredAt
		alert.ResolvedAt = &resolvedAt
		alert.LastObservedAt = ev.OccurredAt

		// 取消该告警正在进行的所有 firing 重试
		s.cancelFiringRetriesForAlert(alert.ID)

		// 1. 先持久化 resolved 状态
		if err := s.repo.SaveAlert(ctx, alert); err != nil {
			return err
		}

		// 2. 如果 firing 曾成功通知过，且当前未被静默，则发送 resolved 通知
		silenced, _ := s.isSilenced(ctx, ev.DeviceID, ev.ReasonCode, ev.OccurredAt)
		if !silenced && alert.FiringNotified {
			notif := s.buildNotification("alert.resolved", alert, "")
			s.dispatchNotification(ctx, notif, alert.ID)
		}

		return nil
	}

	return nil
}

func (s *Service) isSilenced(ctx context.Context, deviceID, reasonCode string, at time.Time) (bool, error) {
	silences, err := s.repo.ListSilences(ctx)
	if err != nil {
		return false, err
	}
	for _, sil := range silences {
		if sil.Matches(deviceID, reasonCode, at) {
			return true, nil
		}
	}
	return false, nil
}

func (s *Service) dispatchNotification(ctx context.Context, notif Notification, alertID string) bool {
	s.mu.RLock()
	channels := make([]Channel, 0, len(s.channels))
	for _, ch := range s.channels {
		channels = append(channels, ch)
	}
	s.mu.RUnlock()

	if len(channels) == 0 {
		return false
	}

	anyDelivered := false

	for _, ch := range channels {
		attemptStart := time.Now().UTC()
		res := ch.Deliver(ctx, notif)
		attemptEnd := time.Now().UTC()

		delivered := !res.Retryable && (res.StatusCode >= 200 && res.StatusCode < 300)
		attempt := DeliveryAttempt{
			ID:            fmt.Sprintf("dlv_%s_%d_1", ch.ID(), attemptStart.UnixNano()),
			AlertID:       alertID,
			ChannelID:     ch.ID(),
			Event:         notif.Event,
			DeliveryID:    notif.DeliveryID,
			AttemptNumber: 1,
			StartedAt:     attemptStart,
			FinishedAt:    attemptEnd,
			StatusCode:    res.StatusCode,
			ErrorCode:     res.ErrorCode,
			ErrorMessage:  res.ErrorMessage,
			Delivered:     delivered,
		}

		if delivered {
			if err := s.repo.RecordDeliveryAttempt(ctx, attempt); err != nil {
				slog.Warn("failed to record delivery attempt", "channel_id", ch.ID(), "error", err)
			}
			anyDelivered = true
		} else if res.Retryable {
			delay := s.backoffSchedule(1, res.RetryAfter)
			next := attemptEnd.Add(delay)
			attempt.NextRetryAt = &next
			if err := s.repo.RecordDeliveryAttempt(ctx, attempt); err != nil {
				slog.Warn("failed to record delivery attempt", "channel_id", ch.ID(), "error", err)
			}
			// 调度后台异步重试 Worker
			s.startAsyncRetry(alertID, ch.ID(), notif, 2, delay)
		} else {
			if err := s.repo.RecordDeliveryAttempt(ctx, attempt); err != nil {
				slog.Warn("failed to record delivery attempt", "channel_id", ch.ID(), "error", err)
			}
		}
	}

	return anyDelivered
}

func (s *Service) startAsyncRetry(alertID, channelID string, notif Notification, nextAttemptNum int, delay time.Duration) {
	key := fmt.Sprintf("%s:%s", alertID, channelID)
	s.cancelRetry(key)

	retryCtx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.activeRetries[key] = activeRetryInfo{cancel: cancel, event: notif.Event}
	s.mu.Unlock()

	go func() {
		select {
		case <-retryCtx.Done():
			return
		case <-time.After(delay):
		}

		s.mu.RLock()
		ch, ok := s.channels[channelID]
		s.mu.RUnlock()
		if !ok {
			s.cancelRetry(key)
			return
		}

		// 事件感知检查：firing 事件要求 alert 仍处于 firing；resolved 事件要求 alert 仍处于 resolved
		alert, err := s.repo.GetAlert(context.Background(), alertID)
		if err != nil || alert == nil {
			s.cancelRetry(key)
			return
		}
		if notif.Event == "alert.firing" && alert.State != AlertFiring {
			s.cancelRetry(key)
			return
		}
		if notif.Event == "alert.resolved" && alert.State != AlertResolved {
			s.cancelRetry(key)
			return
		}

		attemptStart := time.Now().UTC()
		res := ch.Deliver(retryCtx, notif)
		attemptEnd := time.Now().UTC()

		delivered := !res.Retryable && (res.StatusCode >= 200 && res.StatusCode < 300)
		attempt := DeliveryAttempt{
			ID:            fmt.Sprintf("dlv_%s_%d_%d", ch.ID(), attemptStart.UnixNano(), nextAttemptNum),
			AlertID:       alertID,
			ChannelID:     ch.ID(),
			Event:         notif.Event,
			DeliveryID:    notif.DeliveryID,
			AttemptNumber: nextAttemptNum,
			StartedAt:     attemptStart,
			FinishedAt:    attemptEnd,
			StatusCode:    res.StatusCode,
			ErrorCode:     res.ErrorCode,
			ErrorMessage:  res.ErrorMessage,
			Delivered:     delivered,
		}

		if delivered {
			_ = s.repo.RecordDeliveryAttempt(context.Background(), attempt)
			if notif.Event == "alert.firing" {
				alert.FiringNotified = true
				_ = s.repo.SaveAlert(context.Background(), *alert)
			}
			s.cancelRetry(key)
			return
		}

		if !res.Retryable || nextAttemptNum >= 6 {
			_ = s.repo.RecordDeliveryAttempt(context.Background(), attempt)
			s.cancelRetry(key)
			return
		}

		// 计算下一次退避延迟并继续调度
		nextDelay := s.backoffSchedule(nextAttemptNum, res.RetryAfter)
		next := attemptEnd.Add(nextDelay)
		attempt.NextRetryAt = &next
		_ = s.repo.RecordDeliveryAttempt(context.Background(), attempt)

		s.startAsyncRetry(alertID, channelID, notif, nextAttemptNum+1, nextDelay)
	}()
}

func (s *Service) cancelRetry(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if info, exists := s.activeRetries[key]; exists {
		info.cancel()
		delete(s.activeRetries, key)
	}
}

func (s *Service) cancelFiringRetriesForAlert(alertID string) {
	prefix := alertID + ":"
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, info := range s.activeRetries {
		if strings.HasPrefix(k, prefix) && info.event == "alert.firing" {
			info.cancel()
			delete(s.activeRetries, k)
		}
	}
}

// RecoverPendingRetries 从仓储中恢复未完成的异步重试任务（如 Server 重启后）。
func (s *Service) RecoverPendingRetries(ctx context.Context) error {
	pending, err := s.repo.ListPendingRetries(ctx)
	if err != nil {
		return err
	}

	for _, att := range pending {
		alert, err := s.repo.GetAlert(ctx, att.AlertID)
		if err != nil || alert == nil {
			continue
		}

		// 检查状态有效性
		if att.Event == "alert.firing" && alert.State != AlertFiring {
			continue
		}
		if att.Event == "alert.resolved" && alert.State != AlertResolved {
			continue
		}

		delay := time.Duration(0)
		if att.NextRetryAt != nil {
			rem := time.Until(*att.NextRetryAt)
			if rem > 0 {
				delay = rem
			}
		}

		notif := s.buildNotification(att.Event, *alert, att.DeliveryID)
		s.startAsyncRetry(att.AlertID, att.ChannelID, notif, att.AttemptNumber+1, delay)
	}

	return nil
}

func (s *Service) buildNotification(event string, a Alert, deliveryID string) Notification {
	devName := a.DeviceID
	if s.nameResolver != nil {
		if name := s.nameResolver.GetDeviceName(context.Background(), a.DeviceID); name != "" {
			devName = name
		}
	}

	hStatus := health.StatusDegraded
	if event == "alert.resolved" {
		hStatus = health.StatusHealthy
	} else if a.ReasonCode == "device_offline" {
		hStatus = health.StatusOffline
	} else if a.ReasonCode == "device_never_seen" {
		hStatus = health.StatusUnknown
	}

	if deliveryID == "" {
		deliveryID = fmt.Sprintf("dlv_%d_%x", time.Now().UnixNano(), rand.Uint32())
	}

	return Notification{
		SchemaVersion: 1,
		Event:         event,
		DeliveryID:    deliveryID,
		SentAt:        time.Now().UTC(),
		Alert: NotificationAlert{
			ID:              a.ID,
			Status:          string(a.State),
			Severity:        a.Severity,
			ReasonCode:      a.ReasonCode,
			Summary:         a.Summary,
			OpenedAt:        a.OpenedAt,
			ResolvedAt:      a.ResolvedAt,
			SuggestedAction: a.SuggestedAction,
		},
		Device: NotificationDev{
			ID:           a.DeviceID,
			DisplayName:  devName,
			HealthStatus: hStatus,
		},
	}
}

// TestChannel 向指定通道发送一次测试告警。
func (s *Service) TestChannel(ctx context.Context, channelID string) (DeliveryResult, error) {
	s.mu.RLock()
	ch, ok := s.channels[channelID]
	s.mu.RUnlock()

	if !ok {
		return DeliveryResult{}, fmt.Errorf("channel %s not found", channelID)
	}

	now := time.Now().UTC()
	testAlert := Alert{
		ID:              "alt_test",
		DeviceID:        "dev-test",
		ReasonCode:      "channel_test",
		Severity:        health.SeverityInfo,
		State:           AlertFiring,
		OpenedAt:        now,
		Summary:         "HomeAgent 告警通道测试消息",
		SuggestedAction: "无需操作，确认通道投递配置正常即可",
	}

	notif := s.buildNotification("alert.test", testAlert, "")
	res := ch.Deliver(ctx, notif)

	attempt := DeliveryAttempt{
		ID:            fmt.Sprintf("dlv_test_%d", now.UnixNano()),
		AlertID:       "alt_test",
		ChannelID:     channelID,
		Event:         "alert.test",
		DeliveryID:    notif.DeliveryID,
		AttemptNumber: 1,
		StartedAt:     now,
		FinishedAt:    time.Now().UTC(),
		StatusCode:    res.StatusCode,
		ErrorCode:     res.ErrorCode,
		ErrorMessage:  res.ErrorMessage,
		Delivered:     res.StatusCode >= 200 && res.StatusCode < 300,
	}
	_ = s.repo.RecordDeliveryAttempt(ctx, attempt)

	return res, nil
}

// ListAlerts 查询告警列表。
func (s *Service) ListAlerts(ctx context.Context, filter AlertFilter) ([]Alert, string, error) {
	return s.repo.ListAlerts(ctx, filter)
}

// GetAlert 获取单条告警详情。
func (s *Service) GetAlert(ctx context.Context, id string) (*Alert, error) {
	return s.repo.GetAlert(ctx, id)
}

// CreateSilence 创建一条告警静默规则。
func (s *Service) CreateSilence(ctx context.Context, sil Silence) (Silence, error) {
	if sil.EndsAt.Before(sil.StartsAt) {
		return Silence{}, errors.New("ends_at must be after starts_at")
	}
	if sil.ID == "" {
		sil.ID = fmt.Sprintf("sil_%d", time.Now().UnixNano())
	}
	if sil.CreatedAt.IsZero() {
		sil.CreatedAt = time.Now().UTC()
	}
	if err := s.repo.SaveSilence(ctx, sil); err != nil {
		return Silence{}, err
	}
	return sil, nil
}

// DeleteSilence 删除一条静默规则。
func (s *Service) DeleteSilence(ctx context.Context, id string) error {
	return s.repo.DeleteSilence(ctx, id)
}

// ListSilences 列出全部静默规则。
func (s *Service) ListSilences(ctx context.Context) ([]Silence, error) {
	return s.repo.ListSilences(ctx)
}

// ListDeliveryAttempts 查询投递记录列表。
func (s *Service) ListDeliveryAttempts(ctx context.Context, filter DeliveryFilter) ([]DeliveryAttempt, string, error) {
	return s.repo.ListDeliveryAttempts(ctx, filter)
}

func summarizeReason(code string, evidence map[string]any) string {
	switch code {
	case "device_offline":
		return "设备离线，长时间未收到心跳"
	case "heartbeat_stale":
		return "设备心跳陈旧"
	case "agent_version_outdated":
		return "Agent 运行版本过低"
	case "ssh_sync_failed":
		return "SSH 密钥同步失败"
	case "ssh_key_drift":
		return "SSH 密钥存在配置漂移"
	case "ddns_sync_failed":
		return "DDNS 解析同步失败"
	case "ddns_address_drift":
		return "DDNS 地址与公网 IPv6 漂移"
	case "upgrade_failed":
		return "自升级任务执行失败"
	case "disk_space_low":
		return "磁盘剩余可用空间不足"
	case "memory_pressure":
		return "可用内存严重不足"
	default:
		return "设备检测到异常: " + code
	}
}

func suggestAction(code string) string {
	switch code {
	case "device_offline":
		return "检查设备供电、网络与 Agent 守护进程"
	case "heartbeat_stale":
		return "检查设备网络连通性"
	case "agent_version_outdated":
		return "执行自升级更新 Agent 版本"
	case "ssh_sync_failed":
		return "排查 SSH 托管目录权限后重试"
	case "ssh_key_drift":
		return "重新下发 SSH 同步任务"
	case "ddns_sync_failed":
		return "检查 DDNS API Token 权限"
	case "upgrade_failed":
		return "查看升级回执并重试升级"
	case "disk_space_low":
		return "清理临时文件与系统日志"
	case "memory_pressure":
		return "排查高内存占用进程"
	default:
		return "查看设备详情排查异常"
	}
}

