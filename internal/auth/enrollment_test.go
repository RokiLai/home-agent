package auth

import (
	"path/filepath"
	"testing"
	"time"
)

func TestEnrollmentManager_OwnerFreezing(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "enrollment_owner_test.json")

	em, err := NewEnrollmentManager(storePath)
	if err != nil {
		t.Fatalf("NewEnrollmentManager: %v", err)
	}

	// 1. 创建冻结 OwnerUserID 的 Claim Token
	rawToken, tokenInfo, err := em.CreateClaimTokenForOwner(10*time.Minute, 2, "Alice's Pi", "usr_admin_1", "usr_alice")
	if err != nil {
		t.Fatalf("CreateClaimTokenForOwner: %v", err)
	}
	if tokenInfo.CreatedByUserID != "usr_admin_1" || tokenInfo.OwnerUserID != "usr_alice" {
		t.Fatalf("Token owner data unexpected: %+v", tokenInfo)
	}

	// 2. 消费 Claim Token 验证返回的元数据包含冻结的 OwnerUserID
	consumed, err := em.ConsumeClaimToken(rawToken)
	if err != nil {
		t.Fatalf("ConsumeClaimToken failed: %v", err)
	}
	if consumed.OwnerUserID != "usr_alice" || consumed.RemainingUses != 1 {
		t.Fatalf("Consumed token data unexpected: %+v", consumed)
	}

	// 3. 重载持久化验证
	em2, err := NewEnrollmentManager(storePath)
	if err != nil {
		t.Fatalf("Reload EnrollmentManager failed: %v", err)
	}
	activeList := em2.ListActiveTokens()
	if len(activeList) != 1 || activeList[0].OwnerUserID != "usr_alice" {
		t.Fatalf("Reloaded active tokens unexpected: %+v", activeList)
	}
}
