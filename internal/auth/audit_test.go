package auth

import (
	"testing"
	"time"
)

func TestMemoryAuditLogger_RecordAndRecent(t *testing.T) {
	logger := NewMemoryAuditLogger(3)

	logger.Record(AuditEvent{
		ActorUserID:  "usr-1",
		ActorRole:    RoleOwner,
		Action:       ActionAuthLogin,
		ResourceType: "user",
		ResourceID:   "usr-1",
		ClientIP:     "127.0.0.1",
		Status:       "success",
		Detail:       "login via web",
	})

	time.Sleep(10 * time.Millisecond)

	logger.Record(AuditEvent{
		ActorUserID:  "usr-1",
		ActorRole:    RoleOwner,
		Action:       ActionUserCreate,
		ResourceType: "user",
		ResourceID:   "usr-2",
		ClientIP:     "127.0.0.1",
		Status:       "success",
		Detail:       "created user alice",
	})

	recent := logger.Recent(10)
	if len(recent) != 2 {
		t.Fatalf("expected 2 events, got %d", len(recent))
	}
	if recent[0].Action != ActionUserCreate {
		t.Fatalf("expected latest event to be user.create, got %s", recent[0].Action)
	}

	// 超过容量测试
	logger.Record(AuditEvent{Action: ActionDeviceGrant})
	logger.Record(AuditEvent{Action: ActionDeviceTransfer})

	recentOver := logger.Recent(10)
	if len(recentOver) != 3 {
		t.Fatalf("expected capacity limited to 3, got %d", len(recentOver))
	}
	if recentOver[0].Action != ActionDeviceTransfer {
		t.Fatalf("expected latest event to be device.transfer, got %s", recentOver[0].Action)
	}
}
