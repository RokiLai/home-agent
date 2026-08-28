package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMigration_LegacySingleAdminToMultiUser(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "auth_legacy.json")

	// 构造真实旧版 v1 单管理员 auth.json 数据
	passHash, _ := HashPassword("LegacyAdminPass123!")
	sessToken, _ := GenerateSecureToken("agt_sess_", 32)
	sessHash := HashToken(sessToken)
	now := time.Now().UTC().Truncate(time.Second)

	legacyData := map[string]any{
		"admin": map[string]any{
			"username":      "old_admin",
			"password_hash": passHash,
			"created_at":    now,
			"updated_at":    now,
		},
		"sessions": map[string]any{
			sessHash: map[string]any{
				"token_hash":  sessHash,
				"username":    "old_admin",
				"role":        "admin",
				"created_at":  now,
				"expires_at":  now.Add(24 * time.Hour),
				"remember_me": true,
			},
		},
	}

	b, err := json.MarshalIndent(legacyData, "", "  ")
	if err != nil {
		t.Fatalf("Marshal legacy data: %v", err)
	}
	if err := os.WriteFile(storePath, b, 0600); err != nil {
		t.Fatalf("Write legacy file: %v", err)
	}

	// 1. 初始化 SessionManager，触发自动平滑迁移
	sm, err := NewSessionManager(storePath)
	if err != nil {
		t.Fatalf("NewSessionManager with legacy data failed: %v", err)
	}

	// 2. 验证旧管理员密码仍可认证
	owner, err := sm.AuthenticateUser("old_admin", "LegacyAdminPass123!")
	if err != nil {
		t.Fatalf("Authenticate legacy admin user failed: %v", err)
	}
	if owner.Role != RoleOwner || owner.Status != UserStatusActive || owner.SessionVersion != 1 {
		t.Fatalf("Migrated owner state unexpected: %+v", owner)
	}

	// 3. 验证旧 Session 依然有效且 UserID 已绑定
	sess, err := sm.ValidateSession(sessToken)
	if err != nil {
		t.Fatalf("Validate legacy session failed: %v", err)
	}
	if sess.UserID != owner.ID || sess.Username != "old_admin" {
		t.Fatalf("Session mapping unexpected: %+v", sess)
	}

	// 4. 验证备份文件是否存在且内容无损
	bakPath := storePath + ".v1.bak"
	bakBytes, err := os.ReadFile(bakPath)
	if err != nil {
		t.Fatalf("Backup file should exist at %s: %v", bakPath, err)
	}
	if string(bakBytes) != string(b) {
		t.Fatal("Backup file content does not match original legacy data")
	}

	// 5. 验证迁移后落盘的 JSON 文件已升级为 v2 格式且不含旧 admin 结构
	savedBytes, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatalf("Read saved store: %v", err)
	}
	var v2Data authStoreDataV2
	if err := json.Unmarshal(savedBytes, &v2Data); err != nil {
		t.Fatalf("Unmarshal v2 data: %v", err)
	}
	if v2Data.SchemaVersion != 2 || v2Data.Admin != nil || len(v2Data.Users) != 1 {
		t.Fatalf("Saved data is not clean v2 format: %+v", v2Data)
	}

	// 6. 重复加载验证幂等性
	sm2, err := NewSessionManager(storePath)
	if err != nil {
		t.Fatalf("Reload migrated SessionManager failed: %v", err)
	}
	if !sm2.HasAdmin() || sm2.CountActiveOwners() != 1 {
		t.Fatalf("Expected 1 active owner on reload, got %d", sm2.CountActiveOwners())
	}
}
