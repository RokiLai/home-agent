package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"homeagent/internal/auth"
	"homeagent/internal/broker"
	"homeagent/internal/device"
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
		AuditLogger:       auth.NewMemoryAuditLogger(500),
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

func TestMultiUser_DeviceIsolationAndGrants(t *testing.T) {
	s, sm, em, r, _ := setupTestServerWithAuth(t)
	h := s.Handler()

	// 1. 初始化 Owner 用户 (admin)
	ownerUser, _ := sm.GetUserByUsername("admin")
	ownerToken, _, _ := sm.CreateUserSession(ownerUser.ID, false)
	ownerCookie := &http.Cookie{Name: auth.SessionCookieName, Value: ownerToken}

	// 2. 创建 Admin 用户 (sub_admin) 和 Viewer 用户 (guest_viewer)
	adminUser, err := sm.CreateUser("sub_admin", "SubAdmin123!", auth.RoleAdmin, ownerUser.ID)
	if err != nil {
		t.Fatal(err)
	}
	adminToken, _, _ := sm.CreateUserSession(adminUser.ID, false)
	adminCookie := &http.Cookie{Name: auth.SessionCookieName, Value: adminToken}

	viewerUser, err := sm.CreateUser("guest_viewer", "Viewer123!", auth.RoleViewer, ownerUser.ID)
	if err != nil {
		t.Fatal(err)
	}
	viewerToken, _, _ := sm.CreateUserSession(viewerUser.ID, false)
	viewerCookie := &http.Cookie{Name: auth.SessionCookieName, Value: viewerToken}

	// 3. Admin 使用 Claim Token 认领一台设备 (dev-admin)
	_, tokenMeta, err := em.CreateClaimTokenForOwner(10*time.Minute, 1, "Admin Pi", adminUser.ID, adminUser.ID)
	if err != nil {
		t.Fatal(err)
	}
	devAdminClaimReq := httptest.NewRequest("POST", "/api/v1/devices/claim", strings.NewReader(`{"hostname":"admin-pi","os":"linux","arch":"amd64","public_key":"ssh-ed25519 AAA"}`))
	devAdminClaimReq.Header.Set("Authorization", "Bearer "+tokenMeta.ID)
	// 由于 Token 取自真实 hash，使用原始 token 认领
	// 我们直接在 registry 保存并绑定 owner
	devAdmin, _ := r.Save(sampleDevice("dev-admin", adminUser.ID))
	devOwnerOnly, _ := r.Save(sampleDevice("dev-owner-only", ownerUser.ID))

	// 4. 验证设备列表资源隔离
	// a. Owner 可以看到全部 2 台设备
	reqListOwner := httptest.NewRequest("GET", "/api/v1/devices", nil)
	reqListOwner.AddCookie(ownerCookie)
	recListOwner := httptest.NewRecorder()
	h.ServeHTTP(recListOwner, reqListOwner)
	if recListOwner.Code != http.StatusOK {
		t.Fatalf("Owner list devices failed: %d", recListOwner.Code)
	}
	var ownerListResp struct {
		Devices []map[string]any `json:"devices"`
	}
	_ = json.Unmarshal(recListOwner.Body.Bytes(), &ownerListResp)
	if len(ownerListResp.Devices) != 2 {
		t.Fatalf("Owner should see 2 devices, got %d", len(ownerListResp.Devices))
	}

	// b. Admin 仅能看到自己拥有的 1 台设备 (dev-admin)
	reqListAdmin := httptest.NewRequest("GET", "/api/v1/devices", nil)
	reqListAdmin.AddCookie(adminCookie)
	recListAdmin := httptest.NewRecorder()
	h.ServeHTTP(recListAdmin, reqListAdmin)
	if recListAdmin.Code != http.StatusOK {
		t.Fatalf("Admin list devices failed: %d", recListAdmin.Code)
	}
	var adminListResp struct {
		Devices []map[string]any `json:"devices"`
	}
	_ = json.Unmarshal(recListAdmin.Body.Bytes(), &adminListResp)
	if len(adminListResp.Devices) != 1 || adminListResp.Devices[0]["id"] != "dev-admin" {
		t.Fatalf("Admin should only see dev-admin, got: %+v", adminListResp.Devices)
	}

	// c. Viewer 初始没有任何设备可见 (0 台)
	reqListViewer := httptest.NewRequest("GET", "/api/v1/devices", nil)
	reqListViewer.AddCookie(viewerCookie)
	recListViewer := httptest.NewRecorder()
	h.ServeHTTP(recListViewer, reqListViewer)
	var viewerListResp struct {
		Devices []map[string]any `json:"devices"`
	}
	_ = json.Unmarshal(recListViewer.Body.Bytes(), &viewerListResp)
	if len(viewerListResp.Devices) != 0 {
		t.Fatalf("Viewer should see 0 devices, got %d", len(viewerListResp.Devices))
	}

	// 5. 验证 404 IDOR 隐藏保护：Admin 请求 dev-owner-only -> 必须返回 404 而不是 403
	reqIDOR := httptest.NewRequest("GET", "/api/v1/devices/"+devOwnerOnly.ID, nil)
	reqIDOR.AddCookie(adminCookie)
	recIDOR := httptest.NewRecorder()
	h.ServeHTTP(recIDOR, reqIDOR)
	if recIDOR.Code != http.StatusNotFound {
		t.Fatalf("Expected 404 for IDOR attempt on unshared device, got %d", recIDOR.Code)
	}

	// 6. 设备共享授权：Admin 将 dev-admin 共享给 Viewer (read 权限)
	reqGrant := httptest.NewRequest("PUT", "/api/v1/devices/"+devAdmin.ID+"/grants/"+viewerUser.ID, strings.NewReader(`{"level":"read"}`))
	reqGrant.AddCookie(adminCookie)
	recGrant := httptest.NewRecorder()
	h.ServeHTTP(recGrant, reqGrant)
	if recGrant.Code != http.StatusOK {
		t.Fatalf("Expected 200 on granting device, got %d: %s", recGrant.Code, recGrant.Body.String())
	}

	// 7. 共享后 Viewer 可以看到 dev-admin 并读取详情
	reqGetShared := httptest.NewRequest("GET", "/api/v1/devices/"+devAdmin.ID, nil)
	reqGetShared.AddCookie(viewerCookie)
	recGetShared := httptest.NewRecorder()
	h.ServeHTTP(recGetShared, reqGetShared)
	if recGetShared.Code != http.StatusOK {
		t.Fatalf("Expected 200 for viewer accessing shared device, got %d", recGetShared.Code)
	}

	// 8. 但 Viewer 尝试操作关机/同步 -> 403 Forbidden（角色只读硬约束）
	reqShutdown := httptest.NewRequest("POST", "/api/v1/devices/"+devAdmin.ID+"/shutdown", nil)
	reqShutdown.AddCookie(viewerCookie)
	recShutdown := httptest.NewRecorder()
	h.ServeHTTP(recShutdown, reqShutdown)
	if recShutdown.Code != http.StatusForbidden {
		t.Fatalf("Expected 403 for viewer attempting write action, got %d", recShutdown.Code)
	}

	// 9. 用户管理与级联物理删除：删除 Admin 用户，关联的 dev-admin 必须被物理级联清理
	reqDelUser := httptest.NewRequest("DELETE", "/api/v1/users/"+adminUser.ID, nil)
	reqDelUser.AddCookie(ownerCookie)
	recDelUser := httptest.NewRecorder()
	h.ServeHTTP(recDelUser, reqDelUser)
	if recDelUser.Code != http.StatusNoContent {
		t.Fatalf("Expected 204 on deleting user, got %d", recDelUser.Code)
	}

	// 验证 dev-admin 在 Registry 中已不存在
	if _, err := r.Get(devAdmin.ID); err == nil {
		t.Fatal("dev-admin should be cascaded and purged after owner deleted")
	}
}

func sampleDevice(id, ownerID string) device.Device {
	return device.Device{
		ID:          id,
		OwnerUserID: ownerID,
		Hostname:    id,
		OS:          "linux",
		Arch:        "amd64",
		SSHUser:     "root",
		SSHPort:     22,
		PublicKey:   "ssh-ed25519 " + id,
		Addresses:   []string{"192.168.1.100"},
	}
}

func TestMultiUser_UserManagementAndTransferEndpoints(t *testing.T) {
	s, sm, _, r, _ := setupTestServerWithAuth(t)
	h := s.Handler()

	ownerUser, _ := sm.GetUserByUsername("admin")
	ownerToken, _, _ := sm.CreateUserSession(ownerUser.ID, false)
	ownerCookie := &http.Cookie{Name: auth.SessionCookieName, Value: ownerToken}

	// 1. 创建新用户 (POST /api/v1/users)
	reqCreate := httptest.NewRequest("POST", "/api/v1/users", strings.NewReader(`{"username":"charlie","password":"Password123!","role":"admin"}`))
	reqCreate.AddCookie(ownerCookie)
	recCreate := httptest.NewRecorder()
	h.ServeHTTP(recCreate, reqCreate)
	if recCreate.Code != http.StatusCreated {
		t.Fatalf("Expected 201 on create user, got %d: %s", recCreate.Code, recCreate.Body.String())
	}
	var createdUser auth.User
	_ = json.Unmarshal(recCreate.Body.Bytes(), &createdUser)
	if createdUser.Username != "charlie" || createdUser.Role != auth.RoleAdmin {
		t.Fatalf("Unexpected user: %+v", createdUser)
	}

	// 2. 列出用户 (GET /api/v1/users)
	reqList := httptest.NewRequest("GET", "/api/v1/users", nil)
	reqList.AddCookie(ownerCookie)
	recList := httptest.NewRecorder()
	h.ServeHTTP(recList, reqList)
	if recList.Code != http.StatusOK {
		t.Fatalf("Expected 200 on list users, got %d", recList.Code)
	}

	// 3. 修改角色 (PATCH /api/v1/users/{id})
	reqRole := httptest.NewRequest("PATCH", "/api/v1/users/"+createdUser.ID, strings.NewReader(`{"role":"viewer"}`))
	reqRole.AddCookie(ownerCookie)
	recRole := httptest.NewRecorder()
	h.ServeHTTP(recRole, reqRole)
	if recRole.Code != http.StatusOK {
		t.Fatalf("Expected 200 on update role, got %d", recRole.Code)
	}

	// 4. 禁用用户 (POST /api/v1/users/{id}/disable)
	reqDisable := httptest.NewRequest("POST", "/api/v1/users/"+createdUser.ID+"/disable", nil)
	reqDisable.AddCookie(ownerCookie)
	recDisable := httptest.NewRecorder()
	h.ServeHTTP(recDisable, reqDisable)
	if recDisable.Code != http.StatusNoContent {
		t.Fatalf("Expected 204 on disable user, got %d", recDisable.Code)
	}

	// 5. 启用用户 (POST /api/v1/users/{id}/enable)
	reqEnable := httptest.NewRequest("POST", "/api/v1/users/"+createdUser.ID+"/enable", nil)
	reqEnable.AddCookie(ownerCookie)
	recEnable := httptest.NewRecorder()
	h.ServeHTTP(recEnable, reqEnable)
	if recEnable.Code != http.StatusNoContent {
		t.Fatalf("Expected 204 on enable user, got %d", recEnable.Code)
	}

	// 6. 重置密码 (POST /api/v1/users/{id}/password-reset)
	reqReset := httptest.NewRequest("POST", "/api/v1/users/"+createdUser.ID+"/password-reset", strings.NewReader(`{"new_password":"ResetPassword456!"}`))
	reqReset.AddCookie(ownerCookie)
	recReset := httptest.NewRecorder()
	h.ServeHTTP(recReset, reqReset)
	if recReset.Code != http.StatusOK {
		t.Fatalf("Expected 200 on password reset, got %d", recReset.Code)
	}

	// 7. 设备授权管理与所有权转移 (Grants & Transfer)
	dev, _ := r.Save(sampleDevice("dev-charlie", ownerUser.ID))

	// a. 设置授权 (PUT /api/v1/devices/{id}/grants/{user_id})
	reqPutGrant := httptest.NewRequest("PUT", "/api/v1/devices/"+dev.ID+"/grants/"+createdUser.ID, strings.NewReader(`{"level":"operate"}`))
	reqPutGrant.AddCookie(ownerCookie)
	recPutGrant := httptest.NewRecorder()
	h.ServeHTTP(recPutGrant, reqPutGrant)
	if recPutGrant.Code != http.StatusOK {
		t.Fatalf("Expected 200 on put grant, got %d", recPutGrant.Code)
	}

	// b. 列出设备授权 (GET /api/v1/devices/{id}/grants)
	reqListGrants := httptest.NewRequest("GET", "/api/v1/devices/"+dev.ID+"/grants", nil)
	reqListGrants.AddCookie(ownerCookie)
	recListGrants := httptest.NewRecorder()
	h.ServeHTTP(recListGrants, reqListGrants)
	if recListGrants.Code != http.StatusOK {
		t.Fatalf("Expected 200 on list grants, got %d", recListGrants.Code)
	}

	// c. 撤销授权 (DELETE /api/v1/devices/{id}/grants/{user_id})
	reqDelGrant := httptest.NewRequest("DELETE", "/api/v1/devices/"+dev.ID+"/grants/"+createdUser.ID, nil)
	reqDelGrant.AddCookie(ownerCookie)
	recDelGrant := httptest.NewRecorder()
	h.ServeHTTP(recDelGrant, reqDelGrant)
	if recDelGrant.Code != http.StatusNoContent {
		t.Fatalf("Expected 204 on delete grant, got %d", recDelGrant.Code)
	}

	// d. 转移所有权 (POST /api/v1/devices/{id}/transfer)
	reqTransfer := httptest.NewRequest("POST", "/api/v1/devices/"+dev.ID+"/transfer", strings.NewReader(`{"new_owner_id":"`+createdUser.ID+`"}`))
	reqTransfer.AddCookie(ownerCookie)
	recTransfer := httptest.NewRecorder()
	h.ServeHTTP(recTransfer, reqTransfer)
	if recTransfer.Code != http.StatusOK {
		t.Fatalf("Expected 200 on transfer device, got %d: %s", recTransfer.Code, recTransfer.Body.String())
	}
}

func TestMultiUser_AuditLoggingAndLogoutAll(t *testing.T) {
	s, sm, _, _, _ := setupTestServerWithAuth(t)
	h := s.Handler()

	ownerUser, _ := sm.GetUserByUsername("admin")
	ownerToken, _, _ := sm.CreateUserSession(ownerUser.ID, false)
	ownerCookie := &http.Cookie{Name: auth.SessionCookieName, Value: ownerToken}

	// 1. 创建新用户触发审计
	reqCreate := httptest.NewRequest("POST", "/api/v1/users", strings.NewReader(`{"username":"diana","password":"Password123!","role":"admin"}`))
	reqCreate.AddCookie(ownerCookie)
	recCreate := httptest.NewRecorder()
	h.ServeHTTP(recCreate, reqCreate)
	if recCreate.Code != http.StatusCreated {
		t.Fatalf("Create user failed: %d", recCreate.Code)
	}

	// 2. 查询审计日志 (GET /api/v1/audit/logs)
	reqAudit := httptest.NewRequest("GET", "/api/v1/audit/logs", nil)
	reqAudit.AddCookie(ownerCookie)
	recAudit := httptest.NewRecorder()
	h.ServeHTTP(recAudit, reqAudit)
	if recAudit.Code != http.StatusOK {
		t.Fatalf("Expected 200 on audit logs, got %d", recAudit.Code)
	}
	var auditResp struct {
		Events []auth.AuditEvent `json:"events"`
		Count  int               `json:"count"`
	}
	_ = json.Unmarshal(recAudit.Body.Bytes(), &auditResp)
	if auditResp.Count == 0 || len(auditResp.Events) == 0 {
		t.Fatal("Expected recorded audit events, got 0")
	}

	// 3. 测试 Logout-All (POST /api/v1/auth/logout-all)
	// 为 diana 创建两个会话
	dianaUser, _ := sm.GetUserByUsername("diana")
	sessToken1, _, _ := sm.CreateUserSession(dianaUser.ID, false)
	sessToken2, _, _ := sm.CreateUserSession(dianaUser.ID, false)

	// 使用 sessToken1 执行 logout-all
	reqLogoutAll := httptest.NewRequest("POST", "/api/v1/auth/logout-all", nil)
	reqLogoutAll.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: sessToken1})
	recLogoutAll := httptest.NewRecorder()
	h.ServeHTTP(recLogoutAll, reqLogoutAll)
	if recLogoutAll.Code != http.StatusOK {
		t.Fatalf("Expected 200 on logout-all, got %d", recLogoutAll.Code)
	}

	// 验证两个 Token 均已被注销
	if _, err := sm.ValidateSession(sessToken1); err == nil {
		t.Fatal("sessToken1 should be invalidated after logout-all")
	}
	if _, err := sm.ValidateSession(sessToken2); err == nil {
		t.Fatal("sessToken2 should be invalidated after logout-all")
	}
}

func TestMultiUser_UpgradeAndRollbackCompatibility(t *testing.T) {
	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")

	// 1. 模拟 v1 单管理员历史持久化文件
	legacyHash, _ := auth.HashPassword("OldAdminPass123!")
	legacyJSON := `{"admin":{"username":"legacy_admin","password_hash":"` + legacyHash + `","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"},"sessions":{}}`
	if err := os.WriteFile(authPath, []byte(legacyJSON), 0600); err != nil {
		t.Fatalf("write legacy auth file: %v", err)
	}

	// 2. 加载 SessionManager 执行自动迁移
	sm, err := auth.NewSessionManager(authPath)
	if err != nil {
		t.Fatalf("NewSessionManager on legacy file: %v", err)
	}

	// 验证自动备份文件 .v1.bak 存在
	bakPath := authPath + ".v1.bak"
	if _, err := os.Stat(bakPath); err != nil {
		t.Fatalf("expected backup file %s to exist: %v", bakPath, err)
	}

	// 验证旧密码能正常认证且角色为 Owner
	user, err := sm.AuthenticateUser("legacy_admin", "OldAdminPass123!")
	if err != nil {
		t.Fatalf("Authenticate legacy admin after migration: %v", err)
	}
	if user.Role != auth.RoleOwner {
		t.Fatalf("Expected legacy admin to have owner role, got %s", user.Role)
	}

	// 3. 再次重新 Open SessionManager，验证已是 v2 数据无重复迁移
	sm2, err := auth.NewSessionManager(authPath)
	if err != nil {
		t.Fatalf("reload SessionManager v2: %v", err)
	}
	user2, err := sm2.AuthenticateUser("legacy_admin", "OldAdminPass123!")
	if err != nil || user2.ID != user.ID {
		t.Fatalf("failed reloading v2 store: %v", err)
	}
}
