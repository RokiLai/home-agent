package api

import (
	"context"
	"encoding/json"
	"net/http"

	"homeagent/internal/auth"
	"homeagent/internal/device"
)

type claimDeviceReq struct {
	Device *device.Device `json:"device,omitempty"`

	// 平铺字段兼容旧客户端格式
	Hostname  string   `json:"hostname,omitempty"`
	OS        string   `json:"os,omitempty"`
	Arch      string   `json:"arch,omitempty"`
	SSHUser   string   `json:"ssh_user,omitempty"`
	SSHPort   int      `json:"ssh_port,omitempty"`
	PublicKey string   `json:"public_key,omitempty"`
	MAC       string   `json:"mac,omitempty"`
	Addresses []string `json:"addresses,omitempty"`
}

func (s *Server) claimDevice(w http.ResponseWriter, r *http.Request) {
	rawClaimToken := auth.ExtractBearerToken(r)
	if rawClaimToken == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{
			"error":   "unauthorized",
			"message": "Authorization: Bearer <claim_token> required in header",
		})
		return
	}

	// 1. 优先通过 EnrollmentManager 原子校验与扣减 Claim Token 并提取冻结的所有者
	var tokenOwnerID string
	authorized := false
	if s.EnrollmentManager != nil {
		if tokenMeta, err := s.EnrollmentManager.ConsumeClaimToken(rawClaimToken); err == nil {
			authorized = true
			tokenOwnerID = tokenMeta.OwnerUserID
		}
	}

	// 2. 若 Claim Token 校验未通过，检查是否为旧版 HOMEAGENT_JOIN_TOKEN（平滑迁移兼容）
	if !authorized && s.Token != "" {
		if auth.SecureCompareHash(auth.HashToken(s.Token), auth.HashToken(rawClaimToken)) {
			authorized = true
			if s.Log != nil {
				s.Log.Info("legacy_join_token_used_for_claim_migration", "remote", r.RemoteAddr)
			}
		}
	}

	if !authorized {
		writeJSON(w, http.StatusUnauthorized, map[string]string{
			"error":   "unauthorized",
			"message": "Invalid, expired, or exhausted claim token",
		})
		return
	}

	defer r.Body.Close()
	var req claimDeviceReq
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(&req); err != nil {
		http.Error(w, `{"error":"bad_request","message":"Invalid JSON request body"}`, http.StatusBadRequest)
		return
	}

	var d device.Device
	if req.Device != nil {
		d = *req.Device
	} else {
		// 回退到平铺字段
		d = device.Device{
			Hostname:  req.Hostname,
			OS:        req.OS,
			Arch:      req.Arch,
			SSHUser:   req.SSHUser,
			SSHPort:   req.SSHPort,
			PublicKey: req.PublicKey,
			MAC:       req.MAC,
			Addresses: req.Addresses,
		}
	}

	// 确保 device_id 由服务端统一生成
	d.ID = device.GenerateRandomID()

	// 绑定 Token 冻结的所有权（不信任请求体提交的所有者）
	if tokenOwnerID != "" {
		d.OwnerUserID = tokenOwnerID
	}

	// 生成独立的 64 字符 Device Token，并仅将哈希存储在服务端
	rawDeviceToken, err := auth.GenerateSecureToken("dev_", 32)
	if err != nil {
		if s.Log != nil {
			s.Log.Error("generate_device_token_failed", "error", err)
		}
		http.Error(w, `{"error":"server_error","message":"Failed to generate device token"}`, http.StatusInternalServerError)
		return
	}

	d.DeviceTokenHash = auth.HashToken(rawDeviceToken)

	saved, err := s.Registry.Save(d)
	if err != nil {
		if s.Log != nil {
			s.Log.Error("save_claimed_device_failed", "error", err)
		}
		statusError(w, err)
		return
	}

	if s.Log != nil {
		s.Log.Info("device_claimed_successfully", "device_id", saved.ID, "owner_user_id", saved.OwnerUserID, "hostname", saved.Hostname, "os", saved.OS, "arch", saved.Arch)
	}

	if s.Broker != nil {
		s.broadcastKeySync()
	}
	if s.Sync != nil {
		go s.Sync.SyncAll(context.Background())
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"success":          true,
		"device_id":        saved.ID,
		"device_token":     rawDeviceToken,
		"admin_public_key": s.AdminPublicKey,
	})
}
