package store_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"homeagent/internal/auth"
	"homeagent/internal/device"
	"homeagent/internal/store"
	"homeagent/internal/store/filestore"
	"homeagent/internal/store/mysqlstore"
)

func runStoreContractTests(t *testing.T, us store.UserStore, ss store.SessionStore, ds store.DeviceStore, es store.EnrollmentStore, as store.AuditStore) {
	now := time.Now().UTC()

	// 1. 用户增删改查
	u := &auth.User{
		ID:             "usr-contract-1",
		Username:       "Charlie",
		UsernameKey:    "charlie",
		PasswordHash:   "$2a$12$fake",
		Role:           auth.RoleOwner,
		Status:         auth.UserStatusActive,
		SessionVersion: 1,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := us.SaveUser(u); err != nil {
		t.Fatalf("SaveUser failed: %v", err)
	}

	got, err := us.GetUser("usr-contract-1")
	if err != nil || got.Username != "Charlie" {
		t.Fatalf("GetUser contract failure: %v, got: %v", err, got)
	}

	owners, _ := us.CountActiveOwners()
	if owners < 1 {
		t.Fatalf("CountActiveOwners expected >= 1, got %d", owners)
	}

	// 2. 会话管理
	sess := &auth.Session{
		TokenHash:        "token-hash-xyz",
		UserID:           "usr-contract-1",
		Username:         "Charlie",
		Role:             "owner",
		IssuedSessionVer: 1,
		ExpiresAt:        time.Now().UTC().Add(time.Hour),
		CreatedAt:        now,
		LastSeenAt:       now,
	}
	if err := ss.SaveSession(sess); err != nil {
		t.Fatalf("SaveSession failed: %v", err)
	}
	gotSess, err := ss.GetSession("token-hash-xyz")
	if err != nil || gotSess.UserID != "usr-contract-1" {
		t.Fatalf("GetSession failed: %v, got: %v", err, gotSess)
	}

	// 3. 设备与授权
	dev := &device.Device{
		ID:          "dev-contract-1",
		OwnerUserID: "usr-contract-1",
		Hostname:    "server-alpha",
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := ds.SaveDevice(dev); err != nil {
		t.Fatalf("SaveDevice failed: %v", err)
	}

	g := &device.DeviceGrant{
		DeviceID:  "dev-contract-1",
		UserID:    "usr-contract-2",
		Level:     device.GrantLevelManage,
		GrantedBy: "usr-contract-1",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := ds.SaveGrant(g); err != nil {
		t.Fatalf("SaveGrant failed: %v", err)
	}
	grants, err := ds.ListGrants("dev-contract-1")
	if err != nil || len(grants) != 1 {
		t.Fatalf("ListGrants failed: %v, grants: %v", err, grants)
	}

	// 4. Claim Tokens (Enrollment)
	tok := &auth.ClaimToken{
		TokenHash:       "claim-hash-123",
		OwnerUserID:     "usr-contract-1",
		CreatedByUserID: "usr-contract-1",
		Description:     "contract token",
		MaxUses:         1,
		RemainingUses:   1,
		CreatedAt:       now,
		ExpiresAt:       now.Add(time.Hour),
	}
	if err := es.SaveClaimToken(tok); err != nil {
		t.Fatalf("SaveClaimToken failed: %v", err)
	}
	gotTok, err := es.GetClaimToken("claim-hash-123")
	if err != nil || gotTok.OwnerUserID != "usr-contract-1" {
		t.Fatalf("GetClaimToken failed: %v, got: %v", err, gotTok)
	}
	tokList, err := es.ListClaimTokens("usr-contract-1")
	if err != nil || len(tokList) != 1 {
		t.Fatalf("ListClaimTokens failed: %v, len: %d", err, len(tokList))
	}

	// 5. 审计日志
	event := auth.AuditEvent{
		ActorUserID:  "usr-contract-1",
		ActorRole:    auth.RoleOwner,
		Action:       auth.ActionUserCreate,
		ResourceType: "user",
		ResourceID:   "usr-contract-1",
		Status:       "success",
		Detail:       "contract test",
		Timestamp:    now,
	}
	if err := as.Record(event); err != nil {
		t.Fatalf("Record audit failed: %v", err)
	}
	recent, err := as.Recent(10)
	if err != nil || len(recent) == 0 {
		t.Fatalf("Recent audit failed: %v, recent: %v", err, recent)
	}
}

func TestStore_FileStoreContract(t *testing.T) {
	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")
	devPath := filepath.Join(dir, "devices.json")

	fs, err := filestore.NewFileStore(authPath, devPath)
	if err != nil {
		t.Fatalf("NewFileStore failed: %v", err)
	}

	runStoreContractTests(t, fs, fs, fs, fs, fs)
}

func TestStore_MySQLStoreContract(t *testing.T) {
	// 如果本地配置了 MySQL 测试环境（如 rent-mysql），自动执行集成契约测试
	dsn := os.Getenv("HOMEAGENT_TEST_MYSQL_DSN")
	if dsn == "" {
		// 尝试测试直连本地已有的 docker rent-mysql (13306 端口)
		dsn = "root:123456@tcp(127.0.0.1:13306)/homeagent_test?charset=utf8mb4&parseTime=True&loc=Local"
	}

	ms, err := mysqlstore.NewMySQLStore(mysqlstore.Config{DSN: dsn})
	if err != nil {
		t.Skipf("skipping MySQLStore contract test (MySQL not available: %v)", err)
		return
	}
	defer ms.Close()

	// 清理测试库
	_, _ = ms.DB().Exec("DELETE FROM audit_logs")
	_, _ = ms.DB().Exec("DELETE FROM claim_tokens")
	_, _ = ms.DB().Exec("DELETE FROM device_grants")
	_, _ = ms.DB().Exec("DELETE FROM devices")
	_, _ = ms.DB().Exec("DELETE FROM sessions")
	_, _ = ms.DB().Exec("DELETE FROM users")

	runStoreContractTests(t, ms, ms, ms, ms, ms)
}

func TestStore_AutoMigrationFromFileStoreToMySQL(t *testing.T) {
	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")
	devPath := filepath.Join(dir, "devices.json")

	fs, err := filestore.NewFileStore(authPath, devPath)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	// 写入本地数据
	now := time.Now().UTC()
	_ = fs.SaveUser(&auth.User{ID: "usr-mig-1", Username: "mig_user", Role: auth.RoleOwner, Status: auth.UserStatusActive, CreatedAt: now, UpdatedAt: now})
	_ = fs.SaveDevice(&device.Device{ID: "dev-mig-1", OwnerUserID: "usr-mig-1", Hostname: "host-mig", CreatedAt: now, UpdatedAt: now})

	dsn := "root:123456@tcp(127.0.0.1:13306)/homeagent_test?charset=utf8mb4&parseTime=True&loc=Local"
	ms, err := mysqlstore.NewMySQLStore(mysqlstore.Config{DSN: dsn})
	if err != nil {
		t.Skipf("skipping migration test (MySQL not available: %v)", err)
		return
	}
	defer ms.Close()

	// 清空测试库
	_, _ = ms.DB().Exec("DELETE FROM device_grants")
	_, _ = ms.DB().Exec("DELETE FROM devices")
	_, _ = ms.DB().Exec("DELETE FROM users")

	ctx := context.Background()
	migrated, err := store.AutoMigrateFileStoreToMySQL(ctx, fs, fs, ms, ms, authPath, devPath)
	if err != nil {
		t.Fatalf("AutoMigrateFileStoreToMySQL failed: %v", err)
	}
	if !migrated {
		t.Fatal("expected migration to perform")
	}

	// 验证 MySQL 已有数据
	gotUser, err := ms.GetUser("usr-mig-1")
	if err != nil || gotUser.Username != "mig_user" {
		t.Fatalf("expected user in mysql: %v", err)
	}
	gotDev, err := ms.GetDevice("dev-mig-1")
	if err != nil || gotDev.Hostname != "host-mig" {
		t.Fatalf("expected device in mysql: %v", err)
	}

	// 验证本地备份文件存在
	if _, err := os.Stat(authPath + ".migrated.bak"); err != nil {
		t.Fatalf("expected backup auth file to exist: %v", err)
	}
}
