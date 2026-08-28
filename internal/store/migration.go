// Package store 实现存储层平滑迁移
package store

import (
	"context"
	"fmt"
	"os"
)

// AutoMigrateFileStoreToMySQL 在切换至 MySQL 驱动且目标数据库为空时，自动将源存储数据导入目标数据库并备份文件
func AutoMigrateFileStoreToMySQL(ctx context.Context, srcUser UserStore, srcDev DeviceStore, dstUser UserStore, dstDev DeviceStore, authPath, devicesPath string) (bool, error) {
	if srcUser == nil || dstUser == nil || srcDev == nil || dstDev == nil {
		return false, nil
	}

	// 1. 检查目标存储当前是否已有用户或设备
	users, err := dstUser.ListUsers()
	if err != nil {
		return false, fmt.Errorf("check target users: %w", err)
	}
	devices, err := dstDev.ListDevices()
	if err != nil {
		return false, fmt.Errorf("check target devices: %w", err)
	}

	// 若目标存储已经有数据，不进行自动覆盖导入
	if len(users) > 0 || len(devices) > 0 {
		return false, nil
	}

	// 2. 检查源存储是否有数据需要迁移
	localUsers, err := srcUser.ListUsers()
	if err != nil {
		return false, fmt.Errorf("read source users: %w", err)
	}
	localDevices, err := srcDev.ListDevices()
	if err != nil {
		return false, fmt.Errorf("read source devices: %w", err)
	}

	if len(localUsers) == 0 && len(localDevices) == 0 {
		return false, nil
	}

	// 3. 开始迁移数据到目标存储
	for _, u := range localUsers {
		if err := dstUser.SaveUser(u); err != nil {
			return false, fmt.Errorf("migrate user %s: %w", u.Username, err)
		}
	}

	for _, d := range localDevices {
		if err := dstDev.SaveDevice(d); err != nil {
			return false, fmt.Errorf("migrate device %s: %w", d.ID, err)
		}
		grants, _ := srcDev.ListGrants(d.ID)
		for _, g := range grants {
			if err := dstDev.SaveGrant(g); err != nil {
				return false, fmt.Errorf("migrate grant for device %s: %w", d.ID, err)
			}
		}
	}

	// 4. 迁移成功，创建本地快照副本作为备份（保留原文件供本地 sessionMgr 加载）
	if authPath != "" {
		if b, err := os.ReadFile(authPath); err == nil {
			_ = os.WriteFile(authPath+".migrated.bak", b, 0600)
		}
	}
	if devicesPath != "" {
		if b, err := os.ReadFile(devicesPath); err == nil {
			_ = os.WriteFile(devicesPath+".migrated.bak", b, 0600)
		}
	}

	return true, nil
}
