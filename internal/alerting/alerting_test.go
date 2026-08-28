package alerting

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"homeagent/internal/health"
)

type mockChannel struct {
	id         string
	delivered  []Notification
	returnRes  DeliveryResult
}

func (m *mockChannel) ID() string {
	return m.id
}

func (m *mockChannel) Type() string {
	return "mock"
}

func (m *mockChannel) Deliver(ctx context.Context, n Notification) DeliveryResult {
	m.delivered = append(m.delivered, n)
	return m.returnRes
}

func TestAlerting_FingerprintAndSilence(t *testing.T) {
	fp1 := ComputeFingerprint("dev-1", "device_offline")
	fp2 := ComputeFingerprint("dev-1", "device_offline")
	fp3 := ComputeFingerprint("dev-1", "heartbeat_stale")

	if fp1 != fp2 {
		t.Fatalf("expected identical fingerprints, got %s and %s", fp1, fp2)
	}
	if fp1 == fp3 {
		t.Fatalf("expected different fingerprints for different reasons")
	}

	now := time.Now().UTC()
	sil := Silence{
		ID:         "sil-1",
		DeviceID:   "dev-1",
		ReasonCode: "device_offline",
		StartsAt:   now.Add(-10 * time.Minute),
		EndsAt:     now.Add(10 * time.Minute),
	}

	if !sil.Matches("dev-1", "device_offline", now) {
		t.Fatalf("expected silence to match")
	}
	if sil.Matches("dev-2", "device_offline", now) {
		t.Fatalf("silence for dev-1 should not match dev-2")
	}
	if sil.Matches("dev-1", "heartbeat_stale", now) {
		t.Fatalf("silence for device_offline should not match heartbeat_stale")
	}
	if sil.Matches("dev-1", "device_offline", now.Add(20*time.Minute)) {
		t.Fatalf("expired silence should not match")
	}
}

func TestAlerting_LifecycleAndNotifications(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "alerting_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	repo, err := NewFileRepository(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	svc := NewService(ServiceConfig{
		Repo: repo,
	})

	mockCh := &mockChannel{
		id: "ch-1",
		returnRes: DeliveryResult{
			StatusCode: 200,
			Retryable:  false,
		},
	}
	svc.RegisterChannel(mockCh)

	ctx := context.Background()
	now := time.Now().UTC()

	// 1. 发送 opened 事件 -> 触发 firing 告警并分发通知
	evOpened := health.HealthEvent{
		ID:         "hevt-1",
		DeviceID:   "dev-1",
		Type:       "opened",
		ReasonCode: "device_offline",
		Severity:   health.SeverityCritical,
		OccurredAt: now,
	}

	svc.HandleHealthEvents(ctx, []health.HealthEvent{evOpened})

	if len(mockCh.delivered) != 1 {
		t.Fatalf("expected 1 delivered notification, got %d", len(mockCh.delivered))
	}
	if mockCh.delivered[0].Event != "alert.firing" {
		t.Fatalf("expected alert.firing event, got %s", mockCh.delivered[0].Event)
	}

	// 验证 Alert 持久化状态
	fp := ComputeFingerprint("dev-1", "device_offline")
	alert, err := repo.FindActiveAlertByFingerprint(ctx, fp)
	if err != nil || alert == nil {
		t.Fatalf("expected active alert, got %+v", alert)
	}
	if !alert.FiringNotified {
		t.Fatalf("expected FiringNotified to be true")
	}

	// 2. 发送 resolved 事件 -> 触发 resolved 通知并结束告警
	now2 := now.Add(10 * time.Minute)
	evResolved := health.HealthEvent{
		ID:         "hevt-2",
		DeviceID:   "dev-1",
		Type:       "resolved",
		ReasonCode: "device_offline",
		Severity:   health.SeverityCritical,
		OccurredAt: now2,
	}
	svc.HandleHealthEvents(ctx, []health.HealthEvent{evResolved})

	if len(mockCh.delivered) != 2 {
		t.Fatalf("expected 2 delivered notifications, got %d", len(mockCh.delivered))
	}
	if mockCh.delivered[1].Event != "alert.resolved" {
		t.Fatalf("expected alert.resolved event, got %s", mockCh.delivered[1].Event)
	}

	// 验证已无 active alert
	activeAfter, _ := repo.FindActiveAlertByFingerprint(ctx, fp)
	if activeAfter != nil {
		t.Fatalf("expected no active alert after resolution, got %+v", activeAfter)
	}
}

func TestAlerting_FiniteRetry(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "alerting_retry_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	repo, _ := NewFileRepository(tmpDir)
	svc := NewService(ServiceConfig{
		Repo: repo,
		BackoffSchedule: func(attemptNum int, retryAfter time.Duration) time.Duration {
			return 5 * time.Millisecond
		},
	})

	attemptsCount := 0
	doneCh := make(chan struct{})
	retryCh := &mockChannelWithCounter{
		id: "ch-retry",
		deliverFn: func(ctx context.Context, n Notification) DeliveryResult {
			attemptsCount++
			if attemptsCount < 3 {
				return DeliveryResult{
					StatusCode: 500,
					Retryable:  true,
					ErrorCode:  "server_error",
				}
			}
			defer close(doneCh)
			return DeliveryResult{
				StatusCode: 200,
				Retryable:  false,
			}
		},
	}
	svc.RegisterChannel(retryCh)

	ev := health.HealthEvent{
		ID:         "hevt-retry",
		DeviceID:   "dev-retry-1",
		Type:       "opened",
		ReasonCode: "disk_space_low",
		Severity:   health.SeverityWarning,
		OccurredAt: time.Now().UTC(),
	}

	svc.HandleHealthEvents(context.Background(), []health.HealthEvent{ev})

	// 等待后台重试完成
	select {
	case <-doneCh:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for async retries, count=%d", attemptsCount)
	}

	// 稍待写盘完成
	time.Sleep(30 * time.Millisecond)

	// 验证重试成功送达 (共尝试 3 次)
	if attemptsCount != 3 {
		t.Fatalf("expected exactly 3 attempts until success, got %d", attemptsCount)
	}

	deliveries, _, _ := repo.ListDeliveryAttempts(context.Background(), DeliveryFilter{ChannelID: "ch-retry"})
	if len(deliveries) != 3 {
		t.Fatalf("expected 3 recorded delivery attempts, got %d", len(deliveries))
	}
}

func TestAlerting_RetryCancellationOnResolved(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "alerting_cancel_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	repo, _ := NewFileRepository(tmpDir)
	svc := NewService(ServiceConfig{
		Repo: repo,
		BackoffSchedule: func(attemptNum int, retryAfter time.Duration) time.Duration {
			return 200 * time.Millisecond
		},
	})

	attemptsCount := 0
	retryCh := &mockChannelWithCounter{
		id: "ch-cancel",
		deliverFn: func(ctx context.Context, n Notification) DeliveryResult {
			attemptsCount++
			return DeliveryResult{
				StatusCode: 500,
				Retryable:  true,
			}
		},
	}
	svc.RegisterChannel(retryCh)

	now := time.Now().UTC()
	evOpened := health.HealthEvent{
		ID:         "hevt-cancel-1",
		DeviceID:   "dev-cancel-1",
		Type:       "opened",
		ReasonCode: "disk_space_low",
		Severity:   health.SeverityWarning,
		OccurredAt: now,
	}
	svc.HandleHealthEvents(context.Background(), []health.HealthEvent{evOpened})

	// 此时第 1 次尝试已失败，进入重试等待 (200ms)
	// 立即发送 resolved 事件，取消 firing 重试
	evResolved := health.HealthEvent{
		ID:         "hevt-cancel-2",
		DeviceID:   "dev-cancel-1",
		Type:       "resolved",
		ReasonCode: "disk_space_low",
		Severity:   health.SeverityWarning,
		OccurredAt: now.Add(10 * time.Millisecond),
	}
	svc.HandleHealthEvents(context.Background(), []health.HealthEvent{evResolved})

	// 等待超过 200ms
	time.Sleep(300 * time.Millisecond)

	// 验证重试已被及时取消，未发生后续无意义的 firing 重试
	if attemptsCount > 1 {
		t.Fatalf("expected retry to be cancelled after resolution, but got %d attempts", attemptsCount)
	}
}

func TestAlerting_CorruptedRepository_SafeFailure(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "alerting_corrupt_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	corruptedPath := tmpDir + "/alerts.json"
	_ = os.WriteFile(corruptedPath, []byte("invalid-alerts-json-data"), 0600)

	// 验证损坏文件安全失败
	_, err = NewFileRepository(tmpDir)
	if err == nil {
		t.Fatal("expected NewFileRepository to safely fail on corrupted alerts file")
	}

	// 验证未知 SchemaVersion != 1 安全失败
	_ = os.WriteFile(corruptedPath, []byte(`{"schema_version":999,"alerts":[]}`), 0600)
	_, err = NewFileRepository(tmpDir)
	if err == nil {
		t.Fatal("expected NewFileRepository to safely fail on unknown schema version 999")
	}
}

func TestAlerting_MultiChannelIndependentRetry(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "alerting_multi_ch_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	repo, _ := NewFileRepository(tmpDir)
	svc := NewService(ServiceConfig{
		Repo: repo,
		BackoffSchedule: func(attemptNum int, retryAfter time.Duration) time.Duration {
			return 10 * time.Millisecond
		},
	})

	var countA, countB int
	var mu sync.Mutex
	doneA := make(chan struct{})
	doneB := make(chan struct{})

	chA := &mockChannelWithCounter{
		id: "ch-a",
		deliverFn: func(ctx context.Context, n Notification) DeliveryResult {
			mu.Lock()
			countA++
			cur := countA
			mu.Unlock()
			if cur < 2 {
				return DeliveryResult{StatusCode: 500, Retryable: true}
			}
			close(doneA)
			return DeliveryResult{StatusCode: 200}
		},
	}

	chB := &mockChannelWithCounter{
		id: "ch-b",
		deliverFn: func(ctx context.Context, n Notification) DeliveryResult {
			mu.Lock()
			countB++
			cur := countB
			mu.Unlock()
			if cur < 3 {
				return DeliveryResult{StatusCode: 500, Retryable: true}
			}
			close(doneB)
			return DeliveryResult{StatusCode: 200}
		},
	}

	svc.RegisterChannel(chA)
	svc.RegisterChannel(chB)

	ev := health.HealthEvent{
		ID:         "hevt-multi",
		DeviceID:   "dev-multi-1",
		Type:       "opened",
		ReasonCode: "disk_space_low",
		Severity:   health.SeverityWarning,
		OccurredAt: time.Now().UTC(),
	}
	svc.HandleHealthEvents(context.Background(), []health.HealthEvent{ev})

	select {
	case <-doneA:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for chA retry")
	}

	select {
	case <-doneB:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for chB retry")
	}

	mu.Lock()
	if countA != 2 || countB != 3 {
		t.Fatalf("expected chA=2, chB=3 attempts, got chA=%d, chB=%d", countA, countB)
	}
	mu.Unlock()
}

func TestAlerting_ResolvedDeliveryRetry(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "alerting_res_retry_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	repo, _ := NewFileRepository(tmpDir)
	svc := NewService(ServiceConfig{
		Repo: repo,
		BackoffSchedule: func(attemptNum int, retryAfter time.Duration) time.Duration {
			return 10 * time.Millisecond
		},
	})

	firingDone := make(chan struct{})
	resolvedDone := make(chan struct{})
	resolvedAttempts := 0

	ch := &mockChannelWithCounter{
		id: "ch-res",
		deliverFn: func(ctx context.Context, n Notification) DeliveryResult {
			if n.Event == "alert.firing" {
				close(firingDone)
				return DeliveryResult{StatusCode: 200}
			}
			if n.Event == "alert.resolved" {
				resolvedAttempts++
				if resolvedAttempts < 2 {
					return DeliveryResult{StatusCode: 500, Retryable: true}
				}
				defer close(resolvedDone)
				return DeliveryResult{StatusCode: 200}
			}
			return DeliveryResult{StatusCode: 200}
		},
	}
	svc.RegisterChannel(ch)

	now := time.Now().UTC()
	evFiring := health.HealthEvent{
		ID:         "hevt-f-1",
		DeviceID:   "dev-res-1",
		Type:       "opened",
		ReasonCode: "disk_space_low",
		Severity:   health.SeverityWarning,
		OccurredAt: now,
	}
	svc.HandleHealthEvents(context.Background(), []health.HealthEvent{evFiring})

	<-firingDone

	evResolved := health.HealthEvent{
		ID:         "hevt-r-1",
		DeviceID:   "dev-res-1",
		Type:       "resolved",
		ReasonCode: "disk_space_low",
		Severity:   health.SeverityWarning,
		OccurredAt: now.Add(time.Minute),
	}
	svc.HandleHealthEvents(context.Background(), []health.HealthEvent{evResolved})

	select {
	case <-resolvedDone:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for resolved notification retry, attempts=%d", resolvedAttempts)
	}

	if resolvedAttempts != 2 {
		t.Fatalf("expected resolved notification to succeed on 2nd attempt, got %d", resolvedAttempts)
	}
}

func TestAlerting_ServerRestartPendingRetryRecovery(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "alerting_restart_recovery_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	repo1, _ := NewFileRepository(tmpDir)

	now := time.Now().UTC()
	alert := Alert{
		ID:                   "alt-rec-1",
		Fingerprint:          ComputeFingerprint("dev-rec-1", "disk_space_low"),
		DeviceID:             "dev-rec-1",
		ReasonCode:           "disk_space_low",
		Severity:             health.SeverityWarning,
		State:                AlertFiring,
		OpenedAt:             now.Add(-10 * time.Minute),
		LastObservedAt:       now,
		Summary:              "磁盘空间不足",
		FiringNotified:       false,
		NotificationRevision: 1,
	}
	_ = repo1.SaveAlert(context.Background(), alert)

	nextRetry := now.Add(5 * time.Millisecond)
	attempt1 := DeliveryAttempt{
		ID:            "dlv_rec_1",
		AlertID:       "alt-rec-1",
		ChannelID:     "ch-rec",
		Event:         "alert.firing",
		DeliveryID:    "dlv_original_firing_123",
		AttemptNumber: 1,
		StartedAt:     now,
		FinishedAt:    now,
		StatusCode:    500,
		ErrorCode:     "server_error",
		Delivered:     false,
		NextRetryAt:   &nextRetry,
	}
	_ = repo1.RecordDeliveryAttempt(context.Background(), attempt1)

	// 重启 Server：初始化新 Repo 和 Service
	repo2, err := NewFileRepository(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	svc2 := NewService(ServiceConfig{
		Repo: repo2,
		BackoffSchedule: func(attemptNum int, retryAfter time.Duration) time.Duration {
			return 10 * time.Millisecond
		},
	})

	recoveredDone := make(chan struct{})
	var receivedDeliveryID string
	ch := &mockChannelWithCounter{
		id: "ch-rec",
		deliverFn: func(ctx context.Context, n Notification) DeliveryResult {
			if n.Alert.ID == "alt-rec-1" && n.Event == "alert.firing" {
				receivedDeliveryID = n.DeliveryID
				defer close(recoveredDone)
				return DeliveryResult{StatusCode: 200}
			}
			return DeliveryResult{StatusCode: 200}
		},
	}
	svc2.RegisterChannel(ch)

	// 执行启动恢复
	if err := svc2.RecoverPendingRetries(context.Background()); err != nil {
		t.Fatalf("RecoverPendingRetries failed: %v", err)
	}

	select {
	case <-recoveredDone:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for pending retry recovery after restart")
	}

	// 确认重启后依然保留原有的 delivery_id，保证接收方幂等
	if receivedDeliveryID != "dlv_original_firing_123" {
		t.Fatalf("expected delivery_id to be preserved as 'dlv_original_firing_123', got %q", receivedDeliveryID)
	}

	// 稍待写盘
	time.Sleep(30 * time.Millisecond)

	updatedAlert, _ := repo2.GetAlert(context.Background(), "alt-rec-1")
	if updatedAlert == nil || !updatedAlert.FiringNotified {
		t.Fatalf("expected alert FiringNotified to become true after recovery, got %+v", updatedAlert)
	}
}

func TestAlerting_ResolvedPendingRecovery_NotOverwrittenBySuccessfulFiring(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "alerting_res_not_overwritten_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	repo1, _ := NewFileRepository(tmpDir)
	now := time.Now().UTC()

	resolvedAt := now.Add(-1 * time.Minute)
	alert := Alert{
		ID:                   "alt-res-rec-1",
		Fingerprint:          ComputeFingerprint("dev-res-rec-1", "disk_space_low"),
		DeviceID:             "dev-res-rec-1",
		ReasonCode:           "disk_space_low",
		Severity:             health.SeverityWarning,
		State:                AlertResolved,
		OpenedAt:             now.Add(-10 * time.Minute),
		LastObservedAt:       now,
		ResolvedAt:           &resolvedAt,
		Summary:              "磁盘空间不足",
		FiringNotified:       true,
		NotificationRevision: 2,
	}
	_ = repo1.SaveAlert(context.Background(), alert)

	// 1. Firing 第 1 次失败，第 2 次成功
	firingAttempt1 := DeliveryAttempt{
		ID:            "dlv_f_1",
		AlertID:       "alt-res-rec-1",
		ChannelID:     "ch-res-rec",
		Event:         "alert.firing",
		DeliveryID:    "dlv_firing_999",
		AttemptNumber: 1,
		Delivered:     false,
	}
	_ = repo1.RecordDeliveryAttempt(context.Background(), firingAttempt1)

	firingAttempt2 := DeliveryAttempt{
		ID:            "dlv_f_2",
		AlertID:       "alt-res-rec-1",
		ChannelID:     "ch-res-rec",
		Event:         "alert.firing",
		DeliveryID:    "dlv_firing_999",
		AttemptNumber: 2,
		Delivered:     true, // Firing 成功
	}
	_ = repo1.RecordDeliveryAttempt(context.Background(), firingAttempt2)

	// 2. 随后 Resolved 第 1 次失败，处于待重试状态
	nextRetry := now.Add(5 * time.Millisecond)
	resolvedAttempt1 := DeliveryAttempt{
		ID:            "dlv_r_1",
		AlertID:       "alt-res-rec-1",
		ChannelID:     "ch-res-rec",
		Event:         "alert.resolved",
		DeliveryID:    "dlv_resolved_888",
		AttemptNumber: 1,
		Delivered:     false,
		NextRetryAt:   &nextRetry,
	}
	_ = repo1.RecordDeliveryAttempt(context.Background(), resolvedAttempt1)

	// 3. 重启 Server
	repo2, err := NewFileRepository(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	svc2 := NewService(ServiceConfig{
		Repo: repo2,
		BackoffSchedule: func(attemptNum int, retryAfter time.Duration) time.Duration {
			return 10 * time.Millisecond
		},
	})

	recoveredResolvedDone := make(chan struct{})
	var receivedResolvedDeliveryID string
	ch := &mockChannelWithCounter{
		id: "ch-res-rec",
		deliverFn: func(ctx context.Context, n Notification) DeliveryResult {
			if n.Alert.ID == "alt-res-rec-1" && n.Event == "alert.resolved" {
				receivedResolvedDeliveryID = n.DeliveryID
				defer close(recoveredResolvedDone)
				return DeliveryResult{StatusCode: 200}
			}
			return DeliveryResult{StatusCode: 200}
		},
	}
	svc2.RegisterChannel(ch)

	// 启动恢复
	if err := svc2.RecoverPendingRetries(context.Background()); err != nil {
		t.Fatalf("RecoverPendingRetries failed: %v", err)
	}

	select {
	case <-recoveredResolvedDone:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for resolved retry recovery (was it mistakenly overwritten by firing attempt 2?)")
	}

	if receivedResolvedDeliveryID != "dlv_resolved_888" {
		t.Fatalf("expected resolved delivery ID to be preserved as 'dlv_resolved_888', got %q", receivedResolvedDeliveryID)
	}
}

type mockChannelWithCounter struct {
	id        string
	deliverFn func(context.Context, Notification) DeliveryResult
}

func (m *mockChannelWithCounter) ID() string {
	return m.id
}

func (m *mockChannelWithCounter) Type() string {
	return "mock_counter"
}

func (m *mockChannelWithCounter) Deliver(ctx context.Context, n Notification) DeliveryResult {
	if m.deliverFn != nil {
		return m.deliverFn(ctx, n)
	}
	return DeliveryResult{StatusCode: 200}
}

