package githubsync

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClient_DeviceFlowAndAPIs(t *testing.T) {
	mux := http.NewServeMux()

	// 1. Device Code
	mux.HandleFunc("POST /login/device/code", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.FormValue("client_id") != "test-client" {
			t.Errorf("expected client_id test-client, got %s", r.FormValue("client_id"))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(DeviceCodeResponse{
			DeviceCode:      "dev-1234",
			UserCode:        "ABCD-1234",
			VerificationURI: "https://github.com/login/device",
			ExpiresIn:       900,
			Interval:        5,
		})
	})

	// 2. Poll Token
	pollCount := 0
	mux.HandleFunc("POST /login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		pollCount++
		w.Header().Set("Content-Type", "application/json")
		if pollCount == 1 {
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error":             "authorization_pending",
				"error_description": "The authorization request is still pending.",
			})
			return
		}
		_ = json.NewEncoder(w).Encode(TokenResponse{
			AccessToken: "gho_secret_token_12345",
			TokenType:   "bearer",
			Scope:       "repo,read:user,admin:public_key",
		})
	})

	// 3. User Profile
	mux.HandleFunc("GET /user", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer gho_secret_token_12345" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(GitHubUser{
			Login:     "exampleuser",
			ID:        12345678,
			Name:      "Example User",
			AvatarURL: "https://avatars.githubusercontent.com/u/12345678",
		})
	})

	// 4. Register Key
	mux.HandleFunc("POST /user/keys", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer gho_secret_token_12345" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var req keyCreatePayload
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(keyCreateResponse{
			ID:        99887766,
			Key:       req.Key,
			Title:     req.Title,
			CreatedAt: "2026-08-20T00:00:00Z",
		})
	})

	// 5. Delete Key
	mux.HandleFunc("DELETE /user/keys/99887766", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer gho_secret_token_12345" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	client := NewClient(server.Client())
	client.OAuthBase = server.URL
	client.APIBase = server.URL

	ctx := context.Background()

	// Step 1: Request Device Code
	devCode, err := client.RequestDeviceCode(ctx, "test-client", "repo")
	if err != nil {
		t.Fatalf("unexpected RequestDeviceCode error: %v", err)
	}
	if devCode.UserCode != "ABCD-1234" || devCode.DeviceCode != "dev-1234" {
		t.Fatalf("unexpected device code response: %+v", devCode)
	}

	// Step 2: Poll Access Token (1st attempt -> pending)
	tok, err := client.PollAccessToken(ctx, "test-client", "dev-1234")
	if !errors.Is(err, ErrAuthPending) {
		t.Fatalf("expected ErrAuthPending, got: %v (token: %+v)", err, tok)
	}

	// Step 3: Poll Access Token (2nd attempt -> success)
	tok, err = client.PollAccessToken(ctx, "test-client", "dev-1234")
	if err != nil {
		t.Fatalf("unexpected PollAccessToken error: %v", err)
	}
	if tok.AccessToken != "gho_secret_token_12345" {
		t.Fatalf("unexpected token: %s", tok.AccessToken)
	}

	// Step 4: Get Profile
	user, err := client.GetUserProfile(ctx, tok.AccessToken)
	if err != nil {
		t.Fatalf("unexpected GetUserProfile error: %v", err)
	}
	if user.Login != "exampleuser" || user.ID != 12345678 {
		t.Fatalf("unexpected user profile: %+v", user)
	}

	// Step 5: Register Public Key
	keyID, err := client.RegisterPublicKey(ctx, tok.AccessToken, "homeagent-macbook", "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGf/fake test")
	if err != nil {
		t.Fatalf("unexpected RegisterPublicKey error: %v", err)
	}
	if keyID != 99887766 {
		t.Fatalf("expected key ID 99887766, got %d", keyID)
	}

	// Step 6: Delete Public Key
	if err := client.DeletePublicKey(ctx, tok.AccessToken, keyID); err != nil {
		t.Fatalf("unexpected DeletePublicKey error: %v", err)
	}
}
