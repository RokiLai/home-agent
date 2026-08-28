package githubsync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestService_Lifecycle(t *testing.T) {
	tempDir := t.TempDir()

	registeredKeys := make(map[int64]string)
	var keySeq int64 = 100

	mux := http.NewServeMux()
	mux.HandleFunc("POST /login/device/code", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(DeviceCodeResponse{
			DeviceCode:      "dev-999",
			UserCode:        "USER-CODE-999",
			VerificationURI: "https://github.com/login/device",
			ExpiresIn:       60,
			Interval:        1,
		})
	})
	mux.HandleFunc("POST /login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(TokenResponse{
			AccessToken: "gho_test_token_9999",
			TokenType:   "bearer",
			Scope:       DefaultScope,
		})
	})
	mux.HandleFunc("GET /user", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(GitHubUser{
			Login: "tester",
			ID:    4321,
			Name:  "Test User",
		})
	})
	mux.HandleFunc("POST /user/keys", func(w http.ResponseWriter, r *http.Request) {
		keySeq++
		var req keyCreatePayload
		_ = json.NewDecoder(r.Body).Decode(&req)
		registeredKeys[keySeq] = req.Key
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(keyCreateResponse{
			ID:    keySeq,
			Key:   req.Key,
			Title: req.Title,
		})
	})
	mux.HandleFunc("DELETE /user/keys/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	client := NewClient(server.Client())
	client.OAuthBase = server.URL
	client.APIBase = server.URL

	svc, err := NewService(tempDir, client, nil)
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}

	ctx := context.Background()

	// Initial status -> disconnected
	st := svc.GetStatus(2, 0)
	if st.Connected {
		t.Fatalf("expected disconnected initially")
	}

	// 1. Start Device Flow
	flowCode, err := svc.StartDeviceFlow(ctx)
	if err != nil {
		t.Fatalf("StartDeviceFlow failed: %v", err)
	}
	if flowCode.UserCode != "USER-CODE-999" {
		t.Fatalf("unexpected user code: %s", flowCode.UserCode)
	}

	// Status should reflect in_device_flow
	st = svc.GetStatus(2, 0)
	if !st.InDeviceFlow || st.DeviceUserCode != "USER-CODE-999" {
		t.Fatalf("expected in_device_flow, got: %+v", st)
	}

	// 2. Poll & Save Device Flow
	creds, err := svc.PollAndSaveDeviceFlow(ctx)
	if err != nil {
		t.Fatalf("PollAndSaveDeviceFlow failed: %v", err)
	}
	if creds.User.Login != "tester" || creds.Auth.AccessToken != "gho_test_token_9999" {
		t.Fatalf("unexpected creds: %+v", creds)
	}
	if !svc.IsConnected() {
		t.Fatalf("expected IsConnected to be true")
	}

	// Verify files created with 0600
	info, err := os.Stat(svc.CredsPath)
	if err != nil {
		t.Fatalf("stat creds path: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("expected 0600 permissions, got %v", info.Mode().Perm())
	}

	// 3. Register Device Key
	keyID, err := svc.RegisterDeviceKey(ctx, "device-1", "macbook", "ssh-ed25519 AAAAC3Nza... test", "SHA256:abcd")
	if err != nil {
		t.Fatalf("RegisterDeviceKey failed: %v", err)
	}
	if keyID != 101 {
		t.Fatalf("expected keyID 101, got %d", keyID)
	}

	// Idempotent second call with same fingerprint
	keyID2, err := svc.RegisterDeviceKey(ctx, "device-1", "macbook", "ssh-ed25519 AAAAC3Nza... test", "SHA256:abcd")
	if err != nil {
		t.Fatalf("RegisterDeviceKey 2nd call failed: %v", err)
	}
	if keyID2 != keyID {
		t.Fatalf("expected idempotent keyID %d, got %d", keyID, keyID2)
	}

	// 4. Resolve Sync Payload
	payload, err := svc.ResolveSyncPayload()
	if err != nil {
		t.Fatalf("ResolveSyncPayload failed: %v", err)
	}
	if payload.GHConfig.User != "tester" || payload.GHConfig.OAuthToken != "gho_test_token_9999" {
		t.Fatalf("unexpected payload: %+v", payload)
	}

	// 5. Reload Service from Disk to verify persistence
	svc2, err := NewService(tempDir, client, nil)
	if err != nil {
		t.Fatalf("NewService reload failed: %v", err)
	}
	if !svc2.IsConnected() {
		t.Fatalf("expected reloaded service to be connected")
	}
	rec, ok := svc2.GetDeviceKey("device-1")
	if !ok || rec.GitHubKeyID != 101 {
		t.Fatalf("failed to recover device key record: %+v", rec)
	}

	// 6. Delete Device Key
	if err := svc.DeleteDeviceKey(ctx, "device-1"); err != nil {
		t.Fatalf("DeleteDeviceKey failed: %v", err)
	}
	if _, ok := svc.GetDeviceKey("device-1"); ok {
		t.Fatalf("expected device-1 key record to be deleted")
	}

	// 7. Disconnect
	deleted, err := svc.Disconnect(ctx)
	if err != nil {
		t.Fatalf("Disconnect failed: %v", err)
	}
	if svc.IsConnected() {
		t.Fatalf("expected disconnected after Disconnect")
	}
	_ = deleted
}
