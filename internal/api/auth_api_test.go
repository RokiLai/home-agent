package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"homeagent/internal/auth"
	"homeagent/internal/broker"
	"homeagent/internal/registry"
)

func setupTestServerWithAuth(t *testing.T) (*Server, *auth.SessionManager, *auth.EnrollmentManager, *registry.Registry, *broker.Broker) {
	dir := t.TempDir()
	r, err := registry.Open(filepath.Join(dir, "devices.json"))
	if err != nil {
		t.Fatalf("open registry: %v", err)
	}
	b := broker.New()
	sm, err := auth.NewSessionManager(filepath.Join(dir, "auth.json"))
	if err != nil {
		t.Fatalf("open session manager: %v", err)
	}
	_, _ = sm.InitAdminBootstrap("admin", "AdminPass123!")

	em, err := auth.NewEnrollmentManager(filepath.Join(dir, "enrollment.json"))
	if err != nil {
		t.Fatalf("open enrollment manager: %v", err)
	}

	rl := auth.NewRateLimiter(5, 15*time.Minute)

	s := &Server{
		Registry:          r,
		Broker:            b,
		SessionManager:    sm,
		EnrollmentManager: em,
		RateLimiter:       rl,
		AdminPublicKey:    "ssh-ed25519 ADMIN_KEY",
		Token:             "legacy_secret_key",
		Log:               slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return s, sm, em, r, b
}

func TestAdminAuthFlow_LoginMeLogout(t *testing.T) {
	s, _, _, _, _ := setupTestServerWithAuth(t)
	h := s.Handler()

	// 1. 错误密码登录 -> 401
	badLoginBody := `{"username":"admin","password":"wrongpassword"}`
	req := httptest.NewRequest("POST", "/api/v1/auth/login", strings.NewReader(badLoginBody))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 on wrong password, got %d", w.Code)
	}

	// 2. 正确密码登录 -> 200 OK 并下发 HttpOnly Cookie
	goodLoginBody := `{"username":"admin","password":"AdminPass123!","remember_me":true}`
	req = httptest.NewRequest("POST", "/api/v1/auth/login", strings.NewReader(goodLoginBody))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on correct login, got %d: %s", w.Code, w.Body.String())
	}

	// 验证 Cookie 设置
	cookies := w.Result().Cookies()
	var sessionCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == auth.SessionCookieName {
			sessionCookie = c
			break
		}
	}
	if sessionCookie == nil || sessionCookie.Value == "" {
		t.Fatal("expected homeagent_session cookie to be set")
	}
	if !sessionCookie.HttpOnly {
		t.Fatal("session cookie must have HttpOnly set")
	}

	// 3. 携带 Cookie 访问 /api/v1/auth/me -> 200 OK
	req = httptest.NewRequest("GET", "/api/v1/auth/me", nil)
	req.AddCookie(sessionCookie)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /auth/me expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var meResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &meResp)
	if meResp["authenticated"] != true || meResp["username"] != "admin" {
		t.Fatalf("unexpected /auth/me payload: %v", meResp)
	}

	// 4. 访问管理员受保护接口 /api/v1/devices -> 200 OK
	req = httptest.NewRequest("GET", "/api/v1/devices", nil)
	req.AddCookie(sessionCookie)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /devices with session cookie expected 200, got %d", w.Code)
	}

	// 5. 无 Cookie 访问受保护接口 -> 401 Unauthorized
	req = httptest.NewRequest("GET", "/api/v1/devices", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("GET /devices without cookie expected 401, got %d", w.Code)
	}

	// 6. 调用 Logout -> 200 OK 并清除 Cookie
	req = httptest.NewRequest("POST", "/api/v1/auth/logout", nil)
	req.AddCookie(sessionCookie)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("POST /auth/logout expected 200, got %d", w.Code)
	}

	// 7. 再次携带旧 Cookie 访问 -> 401 Unauthorized (服务端已 Revoke)
	req = httptest.NewRequest("GET", "/api/v1/devices", nil)
	req.AddCookie(sessionCookie)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("GET /devices with revoked cookie expected 401, got %d", w.Code)
	}
}

func TestEnrollmentTokensAndClaimFlow(t *testing.T) {
	s, sm, em, r, _ := setupTestServerWithAuth(t)
	h := s.Handler()

	rawSessionToken, _, _ := sm.CreateSession("admin", "admin", false)
	sessCookie := &http.Cookie{Name: auth.SessionCookieName, Value: rawSessionToken}

	// 1. 创建 Claim Token
	createReq := `{"ttl_seconds":600,"max_uses":1,"description":"Test MacBook Pro"}`
	req := httptest.NewRequest("POST", "/api/v1/enrollment-tokens", strings.NewReader(createReq))
	req.AddCookie(sessCookie)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 on token creation, got %d: %s", w.Code, w.Body.String())
	}

	var tokenResp struct {
		Token         string `json:"token"`
		RemainingUses int    `json:"remaining_uses"`
		Description   string `json:"description"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &tokenResp); err != nil || tokenResp.Token == "" {
		t.Fatalf("invalid token response: %v", tokenResp)
	}
	claimToken := tokenResp.Token

	// 2. 列出活跃 Claim Tokens
	req = httptest.NewRequest("GET", "/api/v1/enrollment-tokens", nil)
	req.AddCookie(sessCookie)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on list tokens, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Test MacBook Pro") {
		t.Fatalf("expected active token in list: %s", w.Body.String())
	}

	// 3. 设备端使用 Claim Token 执行认领 (POST /api/v1/devices/claim)
	claimBody := `{
		"device": {
			"hostname": "macbook-pro",
			"os": "darwin",
			"arch": "arm64",
			"ssh_user": "exampleuser",
			"ssh_port": 22,
			"public_key": "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIClientPublicKey...",
			"mac": "3c:22:fb:11:22:33",
			"addresses": ["192.168.1.100"]
		}
	}`
	req = httptest.NewRequest("POST", "/api/v1/devices/claim", strings.NewReader(claimBody))
	req.Header.Set("Authorization", "Bearer "+claimToken)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on claim, got %d: %s", w.Code, w.Body.String())
	}

	var claimResult struct {
		Success     bool   `json:"success"`
		DeviceID    string `json:"device_id"`
		DeviceToken string `json:"device_token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &claimResult); err != nil || !claimResult.Success {
		t.Fatalf("invalid claim result: %v", claimResult)
	}
	if claimResult.DeviceID == "" || claimResult.DeviceToken == "" {
		t.Fatalf("expected device_id and device_token returned, got %+v", claimResult)
	}

	// 4. 第二次使用同一个 Claim Token (max_uses=1) 认领 -> 401 Unauthorized
	req = httptest.NewRequest("POST", "/api/v1/devices/claim", strings.NewReader(claimBody))
	req.Header.Set("Authorization", "Bearer "+claimToken)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 on reused claim token, got %d", w.Code)
	}

	// 5. 校验设备在服务端注册表中的存储：保存的是 SHA-256 哈希而不是明文
	savedDev, err := r.Get(claimResult.DeviceID)
	if err != nil {
		t.Fatalf("device not found in registry: %v", err)
	}
	if savedDev.DeviceTokenHash == "" || savedDev.DeviceTokenHash == claimResult.DeviceToken {
		t.Fatalf("expected device token hash stored, got: %s", savedDev.DeviceTokenHash)
	}
	if savedDev.DeviceTokenHash != auth.HashToken(claimResult.DeviceToken) {
		t.Fatalf("hash mismatch: expected %s, got %s", auth.HashToken(claimResult.DeviceToken), savedDev.DeviceTokenHash)
	}

	// 6. 设备使用分配的 DeviceToken 建立 SSE 订阅与发送 ACK
	req = httptest.NewRequest("POST", "/api/v1/devices/"+claimResult.DeviceID+"/ack", strings.NewReader(`{"status":"synced"}`))
	req.Header.Set("Authorization", "Bearer "+claimResult.DeviceToken)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on device ack, got %d: %s", w.Code, w.Body.String())
	}

	// 7. 防越权 (IDOR) 验证：设备使用自身 Token 试图访问其他设备 ID 的资源 -> 403 Forbidden
	dev2 := savedDev
	dev2.ID = "other-device-2"
	dev2.DeviceTokenHash = auth.HashToken("other_token_222")
	_, _ = r.Save(dev2)

	reqCross2 := httptest.NewRequest("POST", "/api/v1/devices/other-device-2/ack", strings.NewReader(`{"status":"synced"}`))
	reqCross2.Header.Set("Authorization", "Bearer "+claimResult.DeviceToken)
	wCross2 := httptest.NewRecorder()
	h.ServeHTTP(wCross2, reqCross2)
	if wCross2.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden for cross-device IDOR attempt, got %d: %s", wCross2.Code, wCross2.Body.String())
	}

	// 8. 手动作废 Claim Token API
	rawToken2, tokenInfo2, _ := em.CreateClaimToken(10*time.Minute, 1, "Token to delete")
	reqDel := httptest.NewRequest("DELETE", "/api/v1/enrollment-tokens/"+tokenInfo2.ID, nil)
	reqDel.AddCookie(sessCookie)
	wDel := httptest.NewRecorder()
	h.ServeHTTP(wDel, reqDel)
	if wDel.Code != http.StatusNoContent {
		t.Fatalf("expected 204 on delete enrollment token, got %d", wDel.Code)
	}

	// 作废后无法认领
	reqClaim2 := httptest.NewRequest("POST", "/api/v1/devices/claim", strings.NewReader(claimBody))
	reqClaim2.Header.Set("Authorization", "Bearer "+rawToken2)
	wClaim2 := httptest.NewRecorder()
	h.ServeHTTP(wClaim2, reqClaim2)
	if wClaim2.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 on revoked token claim, got %d", wClaim2.Code)
	}
}

func TestLegacyJoinTokenMigration(t *testing.T) {
	s, _, _, _, _ := setupTestServerWithAuth(t)
	h := s.Handler()

	// 1. 旧版 HOMEAGENT_JOIN_TOKEN 严禁访问 Admin API
	reqAdmin := httptest.NewRequest("GET", "/api/v1/devices", nil)
	reqAdmin.Header.Set("Authorization", "Bearer legacy_secret_key")
	wAdmin := httptest.NewRecorder()
	h.ServeHTTP(wAdmin, reqAdmin)
	if wAdmin.Code != http.StatusUnauthorized {
		t.Fatalf("legacy token must NOT have admin access, got %d", wAdmin.Code)
	}

	// 2. 旧版 HOMEAGENT_JOIN_TOKEN 允许用于 /devices/claim 换取独立 Device Token
	claimBody := `{
		"hostname": "legacy-device",
		"os": "linux",
		"arch": "amd64",
		"ssh_user": "ubuntu",
		"ssh_port": 22,
		"public_key": "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAILegacyDeviceKey..."
	}`
	reqClaim := httptest.NewRequest("POST", "/api/v1/devices/claim", strings.NewReader(claimBody))
	reqClaim.Header.Set("Authorization", "Bearer legacy_secret_key")
	wClaim := httptest.NewRecorder()
	h.ServeHTTP(wClaim, reqClaim)
	if wClaim.Code != http.StatusOK {
		t.Fatalf("expected 200 for legacy claim migration, got %d: %s", wClaim.Code, wClaim.Body.String())
	}

	var res struct {
		Success     bool   `json:"success"`
		DeviceID    string `json:"device_id"`
		DeviceToken string `json:"device_token"`
	}
	_ = json.Unmarshal(wClaim.Body.Bytes(), &res)
	if !res.Success || res.DeviceID == "" || res.DeviceToken == "" {
		t.Fatalf("unexpected migration claim result: %+v", res)
	}

	// 3. 换取后的专属 Device Token 可以正常使用
	reqAck := httptest.NewRequest("POST", "/api/v1/devices/"+res.DeviceID+"/ack", strings.NewReader(`{}`))
	reqAck.Header.Set("Authorization", "Bearer "+res.DeviceToken)
	wAck := httptest.NewRecorder()
	h.ServeHTTP(wAck, reqAck)
	if wAck.Code != http.StatusOK {
		t.Fatalf("expected 200 on device ack with migrated token, got %d: %s", wAck.Code, wAck.Body.String())
	}
}

func TestAuthChangePasswordFlow(t *testing.T) {
	dir := t.TempDir()
	sm, err := auth.NewSessionManager(filepath.Join(dir, "auth_change_pass.json"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = sm.InitAdminBootstrap("admin", "InitialPassword123")
	if err != nil {
		t.Fatal(err)
	}

	s := &Server{
		SessionManager: sm,
		Log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	h := s.Handler()

	// 1. 未登录调用修改密码 -> 401
	unauthReq := httptest.NewRequest("POST", "/api/v1/auth/password", strings.NewReader(`{"old_password":"InitialPassword123","new_password":"NewPassword123"}`))
	unauthRec := httptest.NewRecorder()
	h.ServeHTTP(unauthRec, unauthReq)
	if unauthRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 when unauthorized, got %d", unauthRec.Code)
	}

	// 2. 登录获取 Session
	loginReq := httptest.NewRequest("POST", "/api/v1/auth/login", strings.NewReader(`{"username":"admin","password":"InitialPassword123"}`))
	loginRec := httptest.NewRecorder()
	h.ServeHTTP(loginRec, loginReq)
	if loginRec.Code != http.StatusOK {
		t.Fatalf("expected login 200, got %d", loginRec.Code)
	}
	var sessCookie *http.Cookie
	for _, c := range loginRec.Result().Cookies() {
		if c.Name == auth.SessionCookieName {
			sessCookie = c
			break
		}
	}

	// 3. 错误旧密码 -> 400
	wrongOldReq := httptest.NewRequest("POST", "/api/v1/auth/password", strings.NewReader(`{"old_password":"WrongOldPassword","new_password":"NewPassword123"}`))
	wrongOldReq.AddCookie(sessCookie)
	wrongOldRec := httptest.NewRecorder()
	h.ServeHTTP(wrongOldRec, wrongOldReq)
	if wrongOldRec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on wrong old password, got %d", wrongOldRec.Code)
	}

	// 4. 正确修改密码 -> 200
	changeReq := httptest.NewRequest("POST", "/api/v1/auth/password", strings.NewReader(`{"old_password":"InitialPassword123","new_password":"BrandNewPassword456"}`))
	changeReq.AddCookie(sessCookie)
	changeRec := httptest.NewRecorder()
	h.ServeHTTP(changeRec, changeReq)
	if changeRec.Code != http.StatusOK {
		t.Fatalf("expected 200 on successful password change, got %d: %s", changeRec.Code, changeRec.Body.String())
	}

	// 5. 验证新密码能成功登录
	newLoginReq := httptest.NewRequest("POST", "/api/v1/auth/login", strings.NewReader(`{"username":"admin","password":"BrandNewPassword456"}`))
	newLoginRec := httptest.NewRecorder()
	h.ServeHTTP(newLoginRec, newLoginReq)
	if newLoginRec.Code != http.StatusOK {
		t.Fatalf("expected login with new password to succeed (200), got %d", newLoginRec.Code)
	}

	// 6. 验证旧密码登录失败 -> 401
	oldLoginReq := httptest.NewRequest("POST", "/api/v1/auth/login", strings.NewReader(`{"username":"admin","password":"InitialPassword123"}`))
	oldLoginRec := httptest.NewRecorder()
	h.ServeHTTP(oldLoginRec, oldLoginReq)
	if oldLoginRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected login with old password to fail (401), got %d", oldLoginRec.Code)
	}
}
