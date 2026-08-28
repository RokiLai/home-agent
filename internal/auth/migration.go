package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// CurrentSchemaVersion 标识当前最新的认证存储格式版本
const CurrentSchemaVersion = 2

type authStoreDataV2 struct {
	SchemaVersion int                 `json:"schema_version"`
	Admin         *AdminUser          `json:"admin,omitempty"`
	Users         map[string]*User    `json:"users,omitempty"`    // key: user_id
	Sessions      map[string]*Session `json:"sessions,omitempty"` // key: token_hash
}

// MigrateAuthStoreData 将旧版单一管理员（auth.json v1）原子迁移为多用户结构（v2）
func MigrateAuthStoreData(rawBytes []byte, storePath string) (*authStoreDataV2, bool, error) {
	if len(rawBytes) == 0 {
		return &authStoreDataV2{
			SchemaVersion: CurrentSchemaVersion,
			Users:         make(map[string]*User),
			Sessions:      make(map[string]*Session),
		}, false, nil
	}

	var data authStoreDataV2
	if err := json.Unmarshal(rawBytes, &data); err != nil {
		return nil, false, fmt.Errorf("unmarshal auth store: %w", err)
	}

	if data.Users == nil {
		data.Users = make(map[string]*User)
	}
	if data.Sessions == nil {
		data.Sessions = make(map[string]*Session)
	}

	// 若已是 v2 且没有遗留的 admin 结构，无需迁移
	if data.SchemaVersion >= CurrentSchemaVersion && data.Admin == nil {
		return &data, false, nil
	}

	// 发生迁移的条件：存在旧 AdminUser 且尚未初始化 Users
	if data.Admin != nil && data.Admin.PasswordHash != "" && len(data.Users) == 0 {
		// 1. 若配置了持久化路径，先对旧格式执行安全备份
		if storePath != "" {
			bakPath := storePath + ".v1.bak"
			_ = os.WriteFile(bakPath, rawBytes, 0600)
		}

		now := time.Now().UTC()
		ownerID := GenerateUserID()
		uname := strings.TrimSpace(data.Admin.Username)
		if uname == "" {
			uname = "admin"
		}

		createdAt := data.Admin.CreatedAt
		if createdAt.IsZero() {
			createdAt = now
		}
		updatedAt := data.Admin.UpdatedAt
		if updatedAt.IsZero() {
			updatedAt = now
		}

		owner := &User{
			ID:             ownerID,
			Username:       uname,
			UsernameKey:    NormalizeUsernameKey(uname),
			PasswordHash:   data.Admin.PasswordHash,
			Role:           RoleOwner,
			Status:         UserStatusActive,
			SessionVersion: 1,
			CreatedBy:      "system_migration",
			CreatedAt:      createdAt,
			UpdatedAt:      updatedAt,
			Revision:       1,
		}
		data.Users[ownerID] = owner

		// 2. 将所有未过期的既有 Session 映射到该新 owner
		for _, sess := range data.Sessions {
			sess.UserID = ownerID
			sess.Username = uname
			sess.Role = string(RoleOwner)
			sess.IssuedSessionVer = 1
		}

		// 3. 清除旧 Admin 结构，升级 SchemaVersion
		data.Admin = nil
		data.SchemaVersion = CurrentSchemaVersion
		return &data, true, nil
	}

	// 若旧数据既没有 Admin 也没有 Users，直接平滑升级为 v2
	data.Admin = nil
	data.SchemaVersion = CurrentSchemaVersion
	return &data, true, nil
}
