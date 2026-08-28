package auth

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type mockAuthorizer struct {
	tokens map[string]string // token -> deviceID
}

func (m *mockAuthorizer) AuthorizeDevice(rawToken, deviceID string) error {
	expectedID, ok := m.tokens[rawToken]
	if !ok {
		return ErrDeviceNotFound
	}
	if expectedID != deviceID {
		return ErrDeviceMismatch
	}
	return nil
}

func TestHashAndCompare(t *testing.T) {
	token := "my-secret-token"
	h1 := HashToken(token)
	h2 := HashToken(token)
	if h1 != h2 {
		t.Fatalf("hashes should be deterministic: %s != %s", h1, h2)
	}
	if !SecureCompareHash(h1, h2) {
		t.Fatal("SecureCompareHash should return true")
	}

	pass := "StrongAdminPass123!"
	pHash, err := HashPassword(pass)
	if err != nil {
		t.Fatalf("HashPassword failed: %v", err)
	}
	if !CheckPassword(pHash, pass) {
		t.Fatal("CheckPassword should return true for correct password")
	}
	if CheckPassword(pHash, "wrongpass") {
		t.Fatal("CheckPassword should return false for incorrect password")
	}
}

func TestSessionManager(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "auth_test.json")

	sm, err := NewSessionManager(storePath)
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}

	// 1. Initial Bootstrap
	created, err := sm.InitAdminBootstrap("admin", "first-password")
	if err != nil || !created {
		t.Fatalf("InitAdminBootstrap expected true: %v", err)
	}

	// 2. Second Bootstrap must not overwrite
	created2, err := sm.InitAdminBootstrap("admin", "second-password")
	if err != nil || created2 {
		t.Fatalf("Second bootstrap must not overwrite existing password")
	}
	if err := sm.AuthenticateAdmin("admin", "first-password"); err != nil {
		t.Fatalf("Original password must still work: %v", err)
	}
	if err := sm.AuthenticateAdmin("admin", "second-password"); err == nil {
		t.Fatal("New password from second bootstrap should have been ignored")
	}

	// 3. Create Session (Remember Me = true)
	rawToken, sess, err := sm.CreateSession("admin", "admin", true)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if sess.Username != "admin" || !sess.RememberMe {
		t.Fatalf("Unexpected session data: %+v", sess)
	}

	// 4. Validate Session
	validated, err := sm.ValidateSession(rawToken)
	if err != nil || validated.Username != "admin" {
		t.Fatalf("ValidateSession: %v", err)
	}

	// 5. Reload from persistence
	sm2, err := NewSessionManager(storePath)
	if err != nil {
		t.Fatalf("Reload SessionManager: %v", err)
	}
	validated2, err := sm2.ValidateSession(rawToken)
	if err != nil || validated2.Username != "admin" {
		t.Fatalf("ValidateSession after reload: %v", err)
	}

	// 6. Revoke Session
	if err := sm2.RevokeSession(rawToken); err != nil {
		t.Fatalf("RevokeSession: %v", err)
	}
	if _, err := sm2.ValidateSession(rawToken); err == nil {
		t.Fatal("Revoked session must not validate")
	}
}

func TestEnrollmentManager_AtomicConsume(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "enrollment_test.json")

	em, err := NewEnrollmentManager(storePath)
	if err != nil {
		t.Fatalf("NewEnrollmentManager: %v", err)
	}

	rawToken, tokenInfo, err := em.CreateClaimToken(15*time.Minute, 1, "test-device")
	if err != nil {
		t.Fatalf("CreateClaimToken: %v", err)
	}
	if tokenInfo.RemainingUses != 1 {
		t.Fatalf("Expected RemainingUses=1, got %d", tokenInfo.RemainingUses)
	}

	// 并发双花测试：20 个并发请求抢占同一个 max_uses=1 的 Claim Token
	concurrency := 20
	var wg sync.WaitGroup
	var successCount int32
	var failCount int32

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := em.ConsumeClaimToken(rawToken)
			if err == nil {
				atomic.AddInt32(&successCount, 1)
			} else {
				atomic.AddInt32(&failCount, 1)
			}
		}()
	}
	wg.Wait()

	if successCount != 1 {
		t.Fatalf("Expected exactly 1 successful claim, got %d", successCount)
	}
	if failCount != int32(concurrency-1) {
		t.Fatalf("Expected %d failed claims, got %d", concurrency-1, failCount)
	}

	// 再次消费必定失败
	if _, err := em.ConsumeClaimToken(rawToken); err == nil {
		t.Fatal("Consuming consumed token must return error")
	}
}

func TestEnrollmentManager_TTL(t *testing.T) {
	em, _ := NewEnrollmentManager("")
	rawToken, _, err := em.CreateClaimToken(10*time.Millisecond, 2, "short-lived")
	if err != nil {
		t.Fatalf("CreateClaimToken: %v", err)
	}

	time.Sleep(20 * time.Millisecond)
	if _, err := em.ConsumeClaimToken(rawToken); err == nil {
		t.Fatal("Expired token must fail to consume")
	}
}

func TestRateLimiter(t *testing.T) {
	rl := NewRateLimiter(3, 100*time.Millisecond)
	ip := "192.168.1.100"

	// 2 failures -> not locked
	rl.RecordFailure(ip)
	rl.RecordFailure(ip)
	if locked, _ := rl.IsLocked(ip); locked {
		t.Fatal("Should not be locked after 2 failures")
	}

	// 3rd failure -> locked
	locked, dur := rl.RecordFailure(ip)
	if !locked || dur <= 0 {
		t.Fatal("Should be locked on 3rd failure")
	}
	if isLocked, _ := rl.IsLocked(ip); !isLocked {
		t.Fatal("IsLocked should return true")
	}

	// Wait for unlock
	time.Sleep(120 * time.Millisecond)
	if isLocked, _ := rl.IsLocked(ip); isLocked {
		t.Fatal("Should be unlocked after duration")
	}

	// Record success resets
	rl.RecordFailure(ip)
	rl.RecordSuccess(ip)
	rl.RecordFailure(ip)
	if isLocked, _ := rl.IsLocked(ip); isLocked {
		t.Fatal("Failures count should have been reset by success")
	}
}

func TestRequireAdminMiddleware(t *testing.T) {
	sm, _ := NewSessionManager("")
	_, _ = sm.InitAdminBootstrap("admin", "secret")
	rawToken, _, _ := sm.CreateSession("admin", "admin", false)

	handler := RequireAdmin(sm)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess := GetSessionFromContext(r.Context())
		if sess == nil || sess.Username != "admin" {
			t.Fatal("Session missing in context")
		}
		w.WriteHeader(http.StatusOK)
	}))

	// 1. 无 Cookie -> 401
	req1 := httptest.NewRequest("GET", "/api/v1/devices", nil)
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusUnauthorized {
		t.Fatalf("Expected 401, got %d", rec1.Code)
	}

	// 2. 带有效 Cookie -> 200
	req2 := httptest.NewRequest("GET", "/api/v1/devices", nil)
	req2.AddCookie(&http.Cookie{Name: SessionCookieName, Value: rawToken})
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d", rec2.Code)
	}

	// 3. 作废后 -> 401
	_ = sm.RevokeSession(rawToken)
	req3 := httptest.NewRequest("GET", "/api/v1/devices", nil)
	req3.AddCookie(&http.Cookie{Name: SessionCookieName, Value: rawToken})
	rec3 := httptest.NewRecorder()
	handler.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusUnauthorized {
		t.Fatalf("Expected 401 after revocation, got %d", rec3.Code)
	}
}

func TestRequireDeviceMiddleware_IDORProtection(t *testing.T) {
	mockAuth := &mockAuthorizer{
		tokens: map[string]string{
			"dev_token_A": "device-A",
			"dev_token_B": "device-B",
		},
	}

	handler := RequireDevice(mockAuth)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// 1. Device A 访问自身资源 -> 200 OK
	reqA := httptest.NewRequest("GET", "/api/v1/devices/device-A/events", nil)
	reqA.SetPathValue("id", "device-A")
	reqA.Header.Set("Authorization", "Bearer dev_token_A")
	recA := httptest.NewRecorder()
	handler.ServeHTTP(recA, reqA)
	if recA.Code != http.StatusOK {
		t.Fatalf("Expected 200 for Device A accessing Device A, got %d", recA.Code)
	}

	// 2. Device A 访问 Device B 资源 -> 403 Forbidden (IDOR 防护)
	reqCross := httptest.NewRequest("GET", "/api/v1/devices/device-B/events", nil)
	reqCross.SetPathValue("id", "device-B")
	reqCross.Header.Set("Authorization", "Bearer dev_token_A")
	recCross := httptest.NewRecorder()
	handler.ServeHTTP(recCross, reqCross)
	if recCross.Code != http.StatusForbidden {
		t.Fatalf("Expected 403 Forbidden for IDOR attempt, got %d", recCross.Code)
	}

	// 3. 无效 Token -> 401 Unauthorized
	reqInvalid := httptest.NewRequest("GET", "/api/v1/devices/device-A/events", nil)
	reqInvalid.SetPathValue("id", "device-A")
	reqInvalid.Header.Set("Authorization", "Bearer dev_token_invalid")
	recInvalid := httptest.NewRecorder()
	handler.ServeHTTP(recInvalid, reqInvalid)
	if recInvalid.Code != http.StatusUnauthorized {
		t.Fatalf("Expected 401 for invalid device token, got %d", recInvalid.Code)
	}
}

func TestUpdateAdminPassword(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "auth_update_pass.json")

	sm, err := NewSessionManager(storePath)
	if err != nil {
		t.Fatal(err)
	}

	// 1. Initial admin
	_, err = sm.InitAdminBootstrap("admin", "OldPassword123")
	if err != nil {
		t.Fatal(err)
	}

	// 2. Fail with wrong old password
	if err := sm.UpdateAdminPassword("admin", "WrongOldPassword", "NewPassword123"); err == nil {
		t.Fatal("expected error on wrong old password")
	}

	// 3. Fail with short new password
	if err := sm.UpdateAdminPassword("admin", "OldPassword123", "123"); err == nil {
		t.Fatal("expected error on short new password")
	}

	// 4. Success
	if err := sm.UpdateAdminPassword("admin", "OldPassword123", "NewPassword123"); err != nil {
		t.Fatalf("UpdateAdminPassword failed: %v", err)
	}

	// 5. Verify old password no longer works
	if err := sm.AuthenticateAdmin("admin", "OldPassword123"); err == nil {
		t.Fatal("old password should no longer work")
	}

	// 6. Verify new password works
	if err := sm.AuthenticateAdmin("admin", "NewPassword123"); err != nil {
		t.Fatalf("new password should work: %v", err)
	}

	// 7. Verify persistence after reload
	sm2, err := NewSessionManager(storePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := sm2.AuthenticateAdmin("admin", "NewPassword123"); err != nil {
		t.Fatalf("reloaded session manager should accept new password: %v", err)
	}
}
