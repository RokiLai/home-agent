package mysqlstore

import (
	"context"
	"fmt"
	"testing"
	"time"

	"homeagent/internal/auth"
	"homeagent/internal/device"
	"homeagent/internal/store"
)

func TestMySQLStore_AllCRUD(t *testing.T) {
	dsn := "root:123456@tcp(127.0.0.1:13306)/homeagent_test?charset=utf8mb4&parseTime=True&loc=Local"
	ms, err := NewMySQLStore(Config{DSN: dsn})
	if err != nil {
		t.Skipf("skipping MySQLStore test (MySQL not available: %v)", err)
		return
	}
	defer ms.Close()

	// 清理表
	_, _ = ms.DB().Exec("DELETE FROM audit_logs")
	_, _ = ms.DB().Exec("DELETE FROM claim_tokens")
	_, _ = ms.DB().Exec("DELETE FROM device_grants")
	_, _ = ms.DB().Exec("DELETE FROM devices")
	_, _ = ms.DB().Exec("DELETE FROM sessions")
	_, _ = ms.DB().Exec("DELETE FROM users")

	now := time.Now().UTC()

	// 1. User CRUD
	u := &auth.User{
		ID:             "usr-mysql-1",
		Username:       "Dave",
		UsernameKey:    "dave",
		PasswordHash:   "$2a$12$hash",
		Role:           auth.RoleOwner,
		Status:         auth.UserStatusActive,
		SessionVersion: 1,
		CreatedBy:      "system",
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := ms.SaveUser(u); err != nil {
		t.Fatalf("SaveUser failed: %v", err)
	}

	gotU, err := ms.GetUser("usr-mysql-1")
	if err != nil || gotU.Username != "Dave" {
		t.Fatalf("GetUser failed: %v, got: %v", err, gotU)
	}

	gotByKey, err := ms.GetUserByUsernameKey("dave")
	if err != nil || gotByKey.ID != "usr-mysql-1" {
		t.Fatalf("GetUserByUsernameKey failed: %v", err)
	}

	users, err := ms.ListUsers()
	if err != nil || len(users) != 1 {
		t.Fatalf("ListUsers failed: %v, count: %d", err, len(users))
	}

	// 2. Session CRUD
	sess := &auth.Session{
		TokenHash:        "tok-1",
		UserID:           "usr-mysql-1",
		Username:         "Dave",
		Role:             "owner",
		IssuedSessionVer: 1,
		ExpiresAt:        now.Add(time.Hour),
		CreatedAt:        now,
		LastSeenAt:       now,
		RememberMe:       true,
	}
	if err := ms.SaveSession(sess); err != nil {
		t.Fatalf("SaveSession failed: %v", err)
	}
	gotSess, err := ms.GetSession("tok-1")
	if err != nil || gotSess.UserID != "usr-mysql-1" || !gotSess.RememberMe {
		t.Fatalf("GetSession failed: %v, sess: %v", err, gotSess)
	}

	_ = ms.CleanExpired()

	if err := ms.DeleteSessionsByUser("usr-mysql-1"); err != nil {
		t.Fatalf("DeleteSessionsByUser failed: %v", err)
	}
	if _, err := ms.GetSession("tok-1"); err != store.ErrNotFound {
		t.Fatalf("expected ErrNotFound, got: %v", err)
	}

	// 3. Device & Grants
	dev := &device.Device{
		ID:          "dev-mysql-1",
		OwnerUserID: "usr-mysql-1",
		Hostname:    "host-mysql",
		Alias:       "My Server",
		OS:          "linux",
		Arch:        "amd64",
		Addresses:   []string{"192.168.1.50"},
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := ms.SaveDevice(dev); err != nil {
		t.Fatalf("SaveDevice failed: %v", err)
	}

	gotDev, err := ms.GetDevice("dev-mysql-1")
	if err != nil || gotDev.Alias != "My Server" || len(gotDev.Addresses) != 1 {
		t.Fatalf("GetDevice failed: %v, got: %v", err, gotDev)
	}

	g := &device.DeviceGrant{
		DeviceID:  "dev-mysql-1",
		UserID:    "usr-mysql-2",
		Level:     device.GrantLevelOperate,
		GrantedBy: "usr-mysql-1",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := ms.SaveGrant(g); err != nil {
		t.Fatalf("SaveGrant failed: %v", err)
	}

	grants, err := ms.ListGrants("dev-mysql-1")
	if err != nil || len(grants) != 1 {
		t.Fatalf("ListGrants failed: %v", err)
	}

	gotG, err := ms.GetGrant("dev-mysql-1", "usr-mysql-2")
	if err != nil || gotG.Level != device.GrantLevelOperate {
		t.Fatalf("GetGrant failed: %v", err)
	}

	if err := ms.DeleteGrant("dev-mysql-1", "usr-mysql-2"); err != nil {
		t.Fatalf("DeleteGrant failed: %v", err)
	}

	// 4. Claim Tokens (Enrollment)
	tok := &auth.ClaimToken{
		TokenHash:       "tok-claim-1",
		OwnerUserID:     "usr-mysql-1",
		CreatedByUserID: "usr-mysql-1",
		Description:     "test claim token",
		MaxUses:         5,
		RemainingUses:   5,
		CreatedAt:       now,
		ExpiresAt:       now.Add(24 * time.Hour),
	}
	if err := ms.SaveClaimToken(tok); err != nil {
		t.Fatalf("SaveClaimToken failed: %v", err)
	}
	gotTok, err := ms.GetClaimToken("tok-claim-1")
	if err != nil || gotTok.RemainingUses != 5 {
		t.Fatalf("GetClaimToken failed: %v, got: %v", err, gotTok)
	}
	tokList, err := ms.ListClaimTokens("usr-mysql-1")
	if err != nil || len(tokList) != 1 {
		t.Fatalf("ListClaimTokens failed: %v", err)
	}
	if err := ms.DeleteClaimToken("tok-claim-1"); err != nil {
		t.Fatalf("DeleteClaimToken failed: %v", err)
	}

	// 5. Audit Logs
	event := auth.AuditEvent{
		ActorUserID:  "usr-mysql-1",
		ActorRole:    auth.RoleOwner,
		Action:       auth.ActionUserCreate,
		ResourceType: "user",
		ResourceID:   "usr-mysql-1",
		ClientIP:     "127.0.0.1",
		Status:       "success",
		Detail:       "test audit",
		Timestamp:    now,
	}
	if err := ms.Record(event); err != nil {
		t.Fatalf("Record audit failed: %v", err)
	}
	events, err := ms.Recent(10)
	if err != nil || len(events) != 1 {
		t.Fatalf("Recent audit failed: %v, count: %d", err, len(events))
	}

	// 5. Cleanup
	deletedDevs, err := ms.DeleteDevicesByOwner("usr-mysql-1")
	if err != nil || len(deletedDevs) != 1 {
		t.Fatalf("DeleteDevicesByOwner failed: %v", err)
	}
	if err := ms.DeleteUser("usr-mysql-1"); err != nil {
		t.Fatalf("DeleteUser failed: %v", err)
	}
}

func TestMySQLStore_EmptyConfig(t *testing.T) {
	_, err := NewMySQLStore(Config{})
	if err == nil {
		t.Fatal("expected error on empty DSN")
	}
}

func TestMySQLStoreListUsersReturnsStableNewestFirstOrder(t *testing.T) {
	ms, err := NewMySQLStore(Config{DSN: "root:123456@tcp(127.0.0.1:13306)/homeagent_test?charset=utf8mb4&parseTime=True&loc=Local"})
	if err != nil {
		t.Skipf("skipping MySQLStore test (MySQL not available: %v)", err)
	}
	defer ms.Close()

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	ids := []string{"usr-sort-" + suffix + "-a", "usr-sort-" + suffix + "-b", "usr-sort-" + suffix + "-c"}
	defer func() {
		for _, id := range ids {
			_, _ = ms.DB().Exec("DELETE FROM users WHERE id = ?", id)
		}
	}()
	older := time.Now().UTC().Add(time.Hour)
	newer := older.Add(time.Second)
	for index, id := range ids {
		createdAt := newer
		if index == 0 {
			createdAt = older
		}
		if err := ms.SaveUser(&auth.User{ID: id, Username: id, UsernameKey: id, PasswordHash: "hash", Role: auth.RoleOwner, Status: auth.UserStatusActive, SessionVersion: 1, CreatedAt: createdAt, UpdatedAt: createdAt}); err != nil {
			t.Fatal(err)
		}
	}

	users, err := ms.ListUsers()
	if err != nil {
		t.Fatal(err)
	}
	for index, want := range []string{ids[2], ids[1], ids[0]} {
		if users[index].ID != want {
			t.Fatalf("users[%d] = %q, want %q", index, users[index].ID, want)
		}
	}
}

func TestAutoMigrate_Idempotent(t *testing.T) {
	dsn := "root:123456@tcp(127.0.0.1:13306)/homeagent_test?charset=utf8mb4&parseTime=True&loc=Local"
	ms, err := NewMySQLStore(Config{DSN: dsn})
	if err != nil {
		t.Skipf("skipping AutoMigrate test (MySQL not available: %v)", err)
		return
	}
	defer ms.Close()

	ctx := context.Background()
	if err := AutoMigrate(ctx, ms.DB()); err != nil {
		t.Fatalf("AutoMigrate repeated call failed: %v", err)
	}
}
