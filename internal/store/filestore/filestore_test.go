package filestore

import (
	"path/filepath"
	"testing"
	"time"

	"homeagent/internal/auth"
	"homeagent/internal/device"
	"homeagent/internal/store"
)

func TestFileStore_UserAndSessionOperations(t *testing.T) {
	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")
	devPath := filepath.Join(dir, "devices.json")

	fs, err := NewFileStore(authPath, devPath)
	if err != nil {
		t.Fatalf("NewFileStore failed: %v", err)
	}

	// 1. Save User
	now := time.Now().UTC()
	u := &auth.User{
		ID:             "usr-1",
		Username:       "Alice",
		UsernameKey:    "alice",
		PasswordHash:   "$2a$12$fakehash",
		Role:           auth.RoleOwner,
		Status:         auth.UserStatusActive,
		SessionVersion: 1,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := fs.SaveUser(u); err != nil {
		t.Fatalf("SaveUser failed: %v", err)
	}

	// 2. Get User
	got, err := fs.GetUser("usr-1")
	if err != nil || got.Username != "Alice" {
		t.Fatalf("GetUser failed: %v, got: %v", err, got)
	}

	gotByKey, err := fs.GetUserByUsernameKey("alice")
	if err != nil || gotByKey.ID != "usr-1" {
		t.Fatalf("GetUserByUsernameKey failed: %v", err)
	}

	// 3. Count Owners
	owners, _ := fs.CountActiveOwners()
	if owners != 1 {
		t.Fatalf("expected 1 owner, got %d", owners)
	}

	// 4. Save Session & Get Session
	sess := &auth.Session{
		TokenHash:        "hash-123",
		UserID:           "usr-1",
		Username:         "Alice",
		Role:             "owner",
		IssuedSessionVer: 1,
		ExpiresAt:        time.Now().UTC().Add(time.Hour),
		CreatedAt:        now,
	}
	if err := fs.SaveSession(sess); err != nil {
		t.Fatalf("SaveSession failed: %v", err)
	}
	gotSess, err := fs.GetSession("hash-123")
	if err != nil || gotSess.UserID != "usr-1" {
		t.Fatalf("GetSession failed: %v", err)
	}

	// 5. Delete Session
	if err := fs.DeleteSession("hash-123"); err != nil {
		t.Fatalf("DeleteSession failed: %v", err)
	}
	if _, err := fs.GetSession("hash-123"); err != store.ErrNotFound {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}

	// 6. Delete User
	if err := fs.DeleteUser("usr-1"); err != nil {
		t.Fatalf("DeleteUser failed: %v", err)
	}
	if _, err := fs.GetUser("usr-1"); err != store.ErrNotFound {
		t.Fatalf("expected ErrNotFound after user delete, got %v", err)
	}
}

func TestFileStore_DeviceAndGrantOperations(t *testing.T) {
	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")
	devPath := filepath.Join(dir, "devices.json")

	fs, err := NewFileStore(authPath, devPath)
	if err != nil {
		t.Fatalf("NewFileStore failed: %v", err)
	}

	dev := &device.Device{
		ID:          "dev-1",
		OwnerUserID: "usr-alice",
		Hostname:    "macbook",
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}
	if err := fs.SaveDevice(dev); err != nil {
		t.Fatalf("SaveDevice failed: %v", err)
	}

	// Grant
	g := &device.DeviceGrant{
		DeviceID:  "dev-1",
		UserID:    "usr-bob",
		Level:     device.GrantLevelOperate,
		GrantedBy: "usr-alice",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := fs.SaveGrant(g); err != nil {
		t.Fatalf("SaveGrant failed: %v", err)
	}

	grants, err := fs.ListGrants("dev-1")
	if err != nil || len(grants) != 1 || grants[0].UserID != "usr-bob" {
		t.Fatalf("ListGrants failed: %v, grants: %v", err, grants)
	}

	// Delete Devices by Owner
	deleted, err := fs.DeleteDevicesByOwner("usr-alice")
	if err != nil || len(deleted) != 1 || deleted[0] != "dev-1" {
		t.Fatalf("DeleteDevicesByOwner failed: %v, deleted: %v", err, deleted)
	}
	if _, err := fs.GetDevice("dev-1"); err != store.ErrNotFound {
		t.Fatalf("expected device dev-1 to be deleted, got %v", err)
	}
}

func TestFileStore_ClaimTokenOperations(t *testing.T) {
	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")
	devPath := filepath.Join(dir, "devices.json")

	fs, err := NewFileStore(authPath, devPath)
	if err != nil {
		t.Fatalf("NewFileStore failed: %v", err)
	}

	tok := &auth.ClaimToken{
		TokenHash:       "hash-claim-test",
		OwnerUserID:     "usr-alice",
		CreatedByUserID: "usr-alice",
		Description:     "test",
		MaxUses:         1,
		RemainingUses:   1,
		CreatedAt:       time.Now().UTC(),
		ExpiresAt:       time.Now().UTC().Add(time.Hour),
	}
	if err := fs.SaveClaimToken(tok); err != nil {
		t.Fatalf("SaveClaimToken failed: %v", err)
	}

	got, err := fs.GetClaimToken("hash-claim-test")
	if err != nil || got.OwnerUserID != "usr-alice" {
		t.Fatalf("GetClaimToken failed: %v, got: %v", err, got)
	}

	list, err := fs.ListClaimTokens("usr-alice")
	if err != nil || len(list) != 1 {
		t.Fatalf("ListClaimTokens failed: %v", err)
	}

	if err := fs.DeleteClaimToken("hash-claim-test"); err != nil {
		t.Fatalf("DeleteClaimToken failed: %v", err)
	}
	if _, err := fs.GetClaimToken("hash-claim-test"); err != store.ErrNotFound {
		t.Fatalf("expected ErrNotFound, got: %v", err)
	}
}
