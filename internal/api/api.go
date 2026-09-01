// Package api 实现 HomeAgent 服务端 REST API、Server-Sent Events (SSE) 控制平面流、设备路由与 Web 交互。
package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"homeagent/internal/acl"
	"homeagent/internal/alerting"
	"homeagent/internal/auth"
	"homeagent/internal/broker"
	"homeagent/internal/command"
	"homeagent/internal/daemon/upgrade"
	"homeagent/internal/ddns"
	"homeagent/internal/ddns/providers/cloudflare"
	"homeagent/internal/device"
	"homeagent/internal/devicestate"
	"homeagent/internal/githubrelease"
	"homeagent/internal/githubsync"
	"homeagent/internal/health"
	"homeagent/internal/networkaddr"
	"homeagent/internal/prefixstate"
	"homeagent/internal/registry"
	"homeagent/internal/serverupgrade"
	"homeagent/internal/sshsync"
	"homeagent/internal/ui"
	"homeagent/internal/upgradeplan"
	"homeagent/internal/version"
	"homeagent/internal/wol"
)

// Server 协调服务端 HTTP 路由、SSE 推送、身份鉴权、网络唤醒分发、DDNS 以及设备状态管理。
type Server struct {
	Registry                 *registry.Registry
	Authorizer               *auth.Authorizer
	AuditLogger              auth.AuditLogger
	Broker                   *broker.Broker
	SessionManager           *auth.SessionManager
	EnrollmentManager        *auth.EnrollmentManager
	RateLimiter              *auth.RateLimiter
	ACLPath                  string
	Token, AdminPublicKey    string
	Sync                     *sshsync.Controller
	GitHubSyncService        *githubsync.Service
	Log                      *slog.Logger
	DownloadsDir             string
	ScriptsDir               string
	PublicURL                string
	PingInterval             time.Duration
	DeviceStateService       *devicestate.Service
	PrefixStateService       *prefixstate.Service
	DDNSService              *ddns.Service
	CloudflareClient         *cloudflare.Client
	Domain                   string
	ZoneID                   string
	TTL                      int
	Proxied                  bool
	RateLimitDuration        time.Duration
	RecordComment            string
	AutoDeleteStale          bool
	StaleThreshold           time.Duration
	Commands                 *command.Service
	CommandTimeouts          map[command.Kind]command.TimeoutPolicy
	Health                   *health.Service
	Alerting                 *alerting.Service
	UpgradePlans             *upgradeplan.Service
	MacOSAppUpgradeV2Enabled bool
	UpgradeSource            string
	GitHubRepo               string
	GitHubMirrorPrefix       string
	GitHubReleaseClient      *githubrelease.Client

	version       int64
	wakeRateLimit sync.Map
}

// Handler 构建并返回包含所有 REST API、SSE 控制流及 Web UI 的 HTTP 请求多路复用路由处理器。
func (s *Server) Handler() http.Handler {
	if s.UpgradePlans == nil {
		s.UpgradePlans = upgradeplan.NewService()
	}
	if s.Commands != nil && s.Registry != nil {
		s.recoverCommandProjections()
	}

	if s.GitHubSyncService != nil {
		s.GitHubSyncService.OnCredentialsUpdated = func(creds *githubsync.GitHubCredentials) {
			if s.Log != nil {
				s.Log.Info("github_oauth_complete_broadcasting_sync", "user", creds.User.Login)
			}
			s.broadcastGitHubSync()
		}
	}

	if s.Registry != nil && s.Token != "" {
		s.Registry.SetLegacyJoinToken(s.Token)
	}

	if s.Registry != nil && s.Authorizer == nil {
		s.Authorizer = auth.NewAuthorizer(s.Registry)
	}

	if s.GitHubReleaseClient == nil {
		s.GitHubReleaseClient = githubrelease.NewClient(githubrelease.Config{
			Repo:         s.GitHubRepo,
			MirrorPrefix: s.GitHubMirrorPrefix,
		})
	}
	if s.UpgradeSource == "" {
		s.UpgradeSource = "auto"
	}

	// 统一细粒度权限鉴权中间件
	requirePerm := func(perm auth.Permission, resResolver func(r *http.Request) auth.ResourceRef) func(http.Handler) http.Handler {
		if s.SessionManager != nil {
			return auth.RequirePermission(s.SessionManager, s.Authorizer, perm, resResolver)
		}
		return func(next http.Handler) http.Handler { return auth.Bearer(s.Token, next) }
	}

	// 统一基础 Session 鉴权中间件（用于自身状态、登出等基础操作）
	var requireSession func(http.Handler) http.Handler
	if s.SessionManager != nil {
		requireSession = auth.RequireAdmin(s.SessionManager)
	} else {
		requireSession = func(next http.Handler) http.Handler { return auth.Bearer(s.Token, next) }
	}

	var requireDevice func(http.Handler) http.Handler
	if s.Registry != nil {
		requireDevice = auth.RequireDevice(s.Registry)
	} else {
		requireDevice = func(next http.Handler) http.Handler { return auth.Bearer(s.Token, next) }
	}

	var requireAdminOrDevice func(http.Handler) http.Handler
	if s.SessionManager != nil && s.Registry != nil {
		requireAdminOrDevice = auth.RequireAdminOrDevice(s.SessionManager, s.Registry)
	} else {
		requireAdminOrDevice = func(next http.Handler) http.Handler { return auth.Bearer(s.Token, next) }
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	if s.DownloadsDir != "" {
		mux.Handle("GET /downloads/", http.StripPrefix("/downloads/", http.FileServer(http.Dir(s.DownloadsDir))))
	}
	if s.ScriptsDir != "" {
		mux.HandleFunc("GET /install.sh", func(w http.ResponseWriter, r *http.Request) { http.ServeFile(w, r, s.ScriptsDir+"/install.sh") })
		mux.HandleFunc("GET /install.ps1", func(w http.ResponseWriter, r *http.Request) { http.ServeFile(w, r, s.ScriptsDir+"/install.ps1") })
	}

	// Web Dashboard UI routes
	mux.Handle("GET /static/", http.StripPrefix("/static/", ui.Handler()))
	mux.HandleFunc("GET /{$}", s.indexPage)
	mux.HandleFunc("GET /dashboard", s.indexPage)

	// Public configuration & metadata
	mux.HandleFunc("GET /api/v1/config", s.getConfig)

	// Auth routes (Public)
	mux.HandleFunc("POST /api/v1/auth/login", s.authLogin)

	// Auth routes (Session Protected)
	mux.Handle("GET /api/v1/auth/me", requireSession(http.HandlerFunc(s.authMe)))
	mux.Handle("POST /api/v1/auth/logout", requireSession(http.HandlerFunc(s.authLogout)))
	mux.Handle("POST /api/v1/auth/logout-all", requireSession(http.HandlerFunc(s.authLogoutAll)))
	mux.Handle("POST /api/v1/auth/password", requireSession(http.HandlerFunc(s.authChangePassword)))

	// Security Audit Logs
	mux.Handle("GET /api/v1/audit/logs", requirePerm(auth.PermAuditRead, nil)(http.HandlerFunc(s.handleListAuditLogs)))

	// User Management routes (Multi-User RBAC Protected)
	mux.Handle("GET /api/v1/users", requirePerm(auth.PermUsersRead, nil)(http.HandlerFunc(s.listUsers)))
	mux.Handle("POST /api/v1/users", requirePerm(auth.PermUsersCreate, nil)(http.HandlerFunc(s.createUser)))
	mux.Handle("PATCH /api/v1/users/{id}", requirePerm(auth.PermUsersUpdateRole, nil)(http.HandlerFunc(s.updateUserRole)))
	mux.Handle("POST /api/v1/users/{id}/disable", requirePerm(auth.PermUsersDisable, nil)(http.HandlerFunc(s.disableUser)))
	mux.Handle("POST /api/v1/users/{id}/enable", requirePerm(auth.PermUsersDisable, nil)(http.HandlerFunc(s.enableUser)))
	mux.Handle("POST /api/v1/users/{id}/password-reset", requirePerm(auth.PermUsersResetPassword, nil)(http.HandlerFunc(s.resetUserPassword)))
	mux.Handle("DELETE /api/v1/users/{id}", requirePerm(auth.PermUsersDelete, nil)(http.HandlerFunc(s.deleteUser)))

	// Enrollment / Claim Token Management (RBAC Protected)
	mux.Handle("POST /api/v1/enrollment-tokens", requirePerm(auth.PermDevicesClaimTokenCreate, nil)(http.HandlerFunc(s.createEnrollmentToken)))
	mux.Handle("GET /api/v1/enrollment-tokens", requirePerm(auth.PermDevicesClaimTokenRead, nil)(http.HandlerFunc(s.listEnrollmentTokens)))
	mux.Handle("DELETE /api/v1/enrollment-tokens/{id}", requirePerm(auth.PermDevicesClaimTokenRevoke, nil)(http.HandlerFunc(s.deleteEnrollmentToken)))

	// Device Claim (Claim Token Header Protected)
	mux.HandleFunc("POST /api/v1/devices/claim", s.claimDevice)
	// Legacy Register (Legacy compatibility / Claim Token)
	mux.HandleFunc("POST /api/v1/devices/register", s.register)

	// Device Sharing & Transfer routes
	mux.Handle("GET /api/v1/devices/{id}/grants", requirePerm(auth.PermDevicesShare, auth.ResolveDeviceFromPath)(http.HandlerFunc(s.listDeviceGrants)))
	mux.Handle("PUT /api/v1/devices/{id}/grants/{user_id}", requirePerm(auth.PermDevicesShare, auth.ResolveDeviceFromPath)(http.HandlerFunc(s.putDeviceGrant)))
	mux.Handle("DELETE /api/v1/devices/{id}/grants/{user_id}", requirePerm(auth.PermDevicesShare, auth.ResolveDeviceFromPath)(http.HandlerFunc(s.deleteDeviceGrant)))
	mux.Handle("POST /api/v1/devices/{id}/transfer", requirePerm(auth.PermDevicesTransfer, auth.ResolveDeviceFromPath)(http.HandlerFunc(s.transferDevice)))

	// Device Management routes
	mux.Handle("GET /api/v1/bootstrap/admin-key", requirePerm(auth.PermInstanceSettingsRead, nil)(http.HandlerFunc(s.adminKey)))
	mux.Handle("GET /api/v1/devices", requirePerm(auth.PermDevicesRead, nil)(http.HandlerFunc(s.devices)))
	mux.Handle("GET /api/v1/devices/{id}", requirePerm(auth.PermDevicesRead, auth.ResolveDeviceFromPath)(http.HandlerFunc(s.getDevice)))
	mux.Handle("PATCH /api/v1/devices/{id}", requirePerm(auth.PermDevicesUpdate, auth.ResolveDeviceFromPath)(http.HandlerFunc(s.patchDevice)))
	mux.Handle("DELETE /api/v1/devices/{id}", requirePerm(auth.PermDevicesDelete, auth.ResolveDeviceFromPath)(http.HandlerFunc(s.deleteDevice)))
	mux.Handle("POST /api/v1/devices/{id}/sync", requirePerm(auth.PermDevicesSync, auth.ResolveDeviceFromPath)(http.HandlerFunc(s.syncDevice)))
	mux.Handle("POST /api/v1/devices/{id}/wake", requirePerm(auth.PermDevicesWake, auth.ResolveDeviceFromPath)(http.HandlerFunc(s.wakeDevice)))
	mux.Handle("POST /api/v1/devices/{id}/shutdown", requirePerm(auth.PermDevicesShutdown, auth.ResolveDeviceFromPath)(http.HandlerFunc(s.shutdownDevice)))
	mux.Handle("POST /api/v1/devices/{id}/upgrade", requirePerm(auth.PermDevicesUpgrade, auth.ResolveDeviceFromPath)(http.HandlerFunc(s.upgradeDevice)))
	mux.Handle("POST /api/v1/devices/upgrade-all", requirePerm(auth.PermDevicesUpgrade, nil)(http.HandlerFunc(s.upgradeAll)))
	mux.Handle("POST /api/v1/devices/all/upgrade", requirePerm(auth.PermDevicesUpgrade, nil)(http.HandlerFunc(s.upgradeAll)))
	mux.Handle("POST /api/v1/sync", requirePerm(auth.PermDevicesSync, nil)(http.HandlerFunc(s.syncAll)))
	mux.Handle("GET /api/v1/commands", requirePerm(auth.PermCommandsRead, nil)(http.HandlerFunc(s.listCommands)))
	mux.Handle("GET /api/v1/commands/{id}", requirePerm(auth.PermCommandsRead, nil)(http.HandlerFunc(s.getCommand)))
	mux.Handle("POST /api/v1/commands/{id}/cancel", requirePerm(auth.PermCommandsCancel, nil)(http.HandlerFunc(s.cancelCommand)))
	mux.Handle("GET /api/v1/upgrade-plans", requirePerm(auth.PermDevicesRead, nil)(http.HandlerFunc(s.listUpgradePlans)))
	mux.Handle("GET /api/v1/upgrade-plans/{id}", requirePerm(auth.PermDevicesRead, nil)(http.HandlerFunc(s.getUpgradePlan)))

	// System / Server Info & Self-Upgrade routes
	mux.Handle("GET /api/v1/system/version-check", requirePerm(auth.PermInstanceSettingsRead, nil)(http.HandlerFunc(s.systemVersionCheck)))
	mux.Handle("POST /api/v1/system/upgrade", requirePerm(auth.PermInstanceSettingsManage, nil)(http.HandlerFunc(s.systemUpgrade)))

	// Health & Alerting Management routes
	mux.Handle("GET /api/v1/health/summary", requirePerm(auth.PermHealthRead, nil)(http.HandlerFunc(s.handleHealthSummary)))
	mux.Handle("GET /api/v1/devices/{id}/health", requirePerm(auth.PermHealthRead, auth.ResolveDeviceFromPath)(http.HandlerFunc(s.handleDeviceHealth)))
	mux.Handle("GET /api/v1/devices/{id}/health/events", requirePerm(auth.PermHealthRead, auth.ResolveDeviceFromPath)(http.HandlerFunc(s.handleDeviceHealthEvents)))
	mux.Handle("GET /api/v1/alerts", requirePerm(auth.PermAlertsRead, nil)(http.HandlerFunc(s.handleListAlerts)))
	mux.Handle("GET /api/v1/alerts/{id}", requirePerm(auth.PermAlertsRead, nil)(http.HandlerFunc(s.handleGetAlert)))
	mux.Handle("POST /api/v1/alerts/silences", requirePerm(auth.PermAlertsManage, nil)(http.HandlerFunc(s.handleCreateSilence)))
	mux.Handle("DELETE /api/v1/alerts/silences/{id}", requirePerm(auth.PermAlertsManage, nil)(http.HandlerFunc(s.handleDeleteSilence)))
	mux.Handle("GET /api/v1/alerts/silences", requirePerm(auth.PermAlertsRead, nil)(http.HandlerFunc(s.handleListSilences)))
	mux.Handle("GET /api/v1/alert-deliveries", requirePerm(auth.PermAlertsRead, nil)(http.HandlerFunc(s.handleListAlertDeliveries)))
	mux.Handle("POST /api/v1/alert-channels/{id}/test", requirePerm(auth.PermAlertsManage, nil)(http.HandlerFunc(s.handleTestAlertChannel)))

	// SSE and Control Plane routes (Device Token Protected with IDOR checks)
	mux.Handle("GET /api/v1/devices/{id}/events", requireDevice(http.HandlerFunc(s.deviceEvents)))
	mux.Handle("POST /api/v1/devices/{id}/ack", requireDevice(http.HandlerFunc(s.deviceAck)))
	mux.Handle("GET /api/v1/devices/{id}/keys", requireDevice(http.HandlerFunc(s.deviceKeys)))
	mux.Handle("PUT /api/v1/devices/{id}/facts", requireDevice(http.HandlerFunc(s.putDeviceFacts)))

	// IPv6 & DDNS Control Plane routes (Admin or Device)
	mux.Handle("PUT /api/v1/devices/{id}/network-state", requireAdminOrDevice(http.HandlerFunc(s.putDeviceNetworkState)))
	mux.Handle("GET /api/v1/devices/{id}/network-state", requireAdminOrDevice(http.HandlerFunc(s.getDeviceNetworkState)))
	mux.Handle("GET /api/v1/devices/{id}/ipv6", requireAdminOrDevice(http.HandlerFunc(s.getDeviceIPv6Text)))
	mux.Handle("PUT /api/v1/devices/{id}/network-prefixes", requireDevice(http.HandlerFunc(s.putRouterPrefixes)))
	mux.Handle("GET /api/v1/networks/{id}/prefixes", requirePerm(auth.PermDevicesRead, nil)(http.HandlerFunc(s.getNetworkPrefixes)))

	// GitHub Credential Sync routes (Admin Protected / Device Protected)
	mux.Handle("POST /api/v1/github/auth/device-code", requirePerm(auth.PermGitHubManage, nil)(http.HandlerFunc(s.githubDeviceCode)))
	mux.Handle("GET /api/v1/github/status", requirePerm(auth.PermGitHubManage, nil)(http.HandlerFunc(s.githubStatus)))
	mux.Handle("POST /api/v1/github/disconnect", requirePerm(auth.PermGitHubManage, nil)(http.HandlerFunc(s.githubDisconnect)))
	mux.HandleFunc("GET /api/v1/github/avatar", s.githubAvatar)
	mux.Handle("POST /api/v1/devices/{id}/github/ssh-key", requireDevice(http.HandlerFunc(s.deviceRegisterGitHubSSHKey)))

	return withCORS(mux)
}

type deviceFactsReq struct {
	Hostname                string                `json:"hostname"`
	MAC                     string                `json:"mac,omitempty"`
	AgentVersion            string                `json:"agent_version,omitempty"`
	OS                      string                `json:"os"`
	Arch                    string                `json:"arch"`
	SSHUser                 string                `json:"ssh_user"`
	SSHPort                 int                   `json:"ssh_port"`
	Addresses               []string              `json:"addresses"`
	ControlProtocols        *[]int                `json:"control_protocols,omitempty"`
	UpgradeTransactionID    string                `json:"upgrade_transaction_id,omitempty"`
	UpgradeFenceRevision    *uint64               `json:"upgrade_fence_revision,omitempty"`
	UpgradeFenceToken       string                `json:"upgrade_fence_token,omitempty"`
	UpgradeReleaseSequence  *uint64               `json:"upgrade_release_sequence,omitempty"`
	ConfirmedManifestDigest string                `json:"confirmed_manifest_digest,omitempty"`
	RunningBundleDigest     string                `json:"running_bundle_digest,omitempty"`
	UpgradeSecurityMode     string                `json:"upgrade_security_mode,omitempty"`
	CommandID               string                `json:"command_id,omitempty"`
	Runtime                 *device.RuntimeFacts  `json:"runtime,omitempty"`
}

// putDeviceFacts refreshes mutable host facts using the device's own credential.
func (s *Server) putDeviceFacts(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	var req deviceFactsReq
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		http.Error(w, "invalid JSON request: "+err.Error(), http.StatusBadRequest)
		return
	}

	d, err := s.Registry.Get(r.PathValue("id"))
	if err != nil {
		statusError(w, err)
		return
	}
	d.Hostname = req.Hostname
	d.MAC = req.MAC
	d.AgentVersion = req.AgentVersion
	d.OS = req.OS
	d.Arch = req.Arch
	if req.SSHUser != "" && !strings.HasSuffix(req.SSHUser, "$") {
		d.SSHUser = req.SSHUser
	} else if strings.HasSuffix(req.SSHUser, "$") && s.Log != nil {
		s.Log.Warn("rejected_machine_account_ssh_user", "device_id", d.ID, "ssh_user", req.SSHUser)
	}
	if req.SSHPort > 0 {
		d.SSHPort = req.SSHPort
	}
	d.Addresses = req.Addresses
	if req.ControlProtocols != nil {
		d.ControlProtocols = normalizeProtocols(*req.ControlProtocols)
	}
	if req.UpgradeTransactionID != "" {
		d.UpgradeTransactionID = req.UpgradeTransactionID
	}
	if req.UpgradeFenceRevision != nil {
		d.UpgradeFenceRevision = *req.UpgradeFenceRevision
	}
	if req.UpgradeReleaseSequence != nil {
		d.UpgradeReleaseSequence = *req.UpgradeReleaseSequence
	}
	if req.ConfirmedManifestDigest != "" {
		d.ConfirmedManifestDigest = req.ConfirmedManifestDigest
	}
	if req.RunningBundleDigest != "" {
		d.RunningBundleDigest = req.RunningBundleDigest
	}
	if req.UpgradeSecurityMode != "" {
		d.UpgradeSecurityMode = req.UpgradeSecurityMode
	}
	if req.Runtime != nil {
		rf := req.Runtime
		if rf.MemoryAvailableBytes > rf.MemoryTotalBytes {
			http.Error(w, "invalid runtime facts: memory available cannot exceed total", http.StatusBadRequest)
			return
		}
		if rf.DiskAvailableBytes > rf.DiskTotalBytes {
			http.Error(w, "invalid runtime facts: disk available cannot exceed total", http.StatusBadRequest)
			return
		}
		if rf.Load1 != nil {
			if math.IsNaN(*rf.Load1) || math.IsInf(*rf.Load1, 0) || *rf.Load1 < 0 {
				http.Error(w, "invalid runtime facts: load_1 must be non-negative finite number", http.StatusBadRequest)
				return
			}
		}
		if rf.UptimeSeconds < 0 {
			http.Error(w, "invalid runtime facts: uptime cannot be negative", http.StatusBadRequest)
			return
		}
		if rf.LogicalCPUCount < 0 {
			http.Error(w, "invalid runtime facts: cpu count cannot be negative", http.StatusBadRequest)
			return
		}
		d.RuntimeFacts = req.Runtime
	} else {
		// 当本次上报未附带 runtime 时，不能沿用旧值，必须置空
		d.RuntimeFacts = nil
	}

	saved, err := s.Registry.Save(d)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if s.Health != nil {
		go s.Health.EvaluateDevice(context.Background(), saved.ID)
	}
	if s.Log != nil {
		s.Log.Info("device_facts_updated", "device_id", saved.ID, "addresses", len(saved.Addresses), "mac", saved.MAC, "agent_version", saved.AgentVersion)
	}

	var conf *upgrade.UpgradeConfirmation
	if req.UpgradeFenceRevision != nil && req.UpgradeFenceToken != "" {
		tokenBytes, decErr := base64.RawURLEncoding.DecodeString(req.UpgradeFenceToken)
		if decErr == nil && len(tokenBytes) == 32 {
			fenceDigest := upgrade.ComputeFenceDigest(tokenBytes)
			factsDigest, _ := upgrade.ComputeFactsDigest(upgrade.FactsDigestParams{
				DeviceID:            saved.ID,
				CommandID:           req.CommandID,
				TransactionID:       saved.UpgradeTransactionID,
				TargetVersion:       saved.AgentVersion,
				UpgradeSecurityMode: saved.UpgradeSecurityMode,
				FenceRevision:       *req.UpgradeFenceRevision,
				ReleaseSequence:     saved.UpgradeReleaseSequence,
				FenceTokenDigest:    fenceDigest,
				ManifestDigest:      saved.ConfirmedManifestDigest,
				RunningBundleDigest: saved.RunningBundleDigest,
			})
			nonceBytes := make([]byte, 24)
			_, _ = rand.Read(nonceBytes)
			serverNonce := base64.RawURLEncoding.EncodeToString(nonceBytes)
			conf = &upgrade.UpgradeConfirmation{
				State:         "prepared",
				CommandID:     req.CommandID,
				FenceRevision: *req.UpgradeFenceRevision,
				FenceDigest:   fenceDigest,
				FactsDigest:   factsDigest,
				ServerNonce:   serverNonce,
			}
		}
	}

	dto := s.toDeviceDTO(saved)
	if conf != nil {
		respMap := make(map[string]any)
		b, _ := json.Marshal(dto)
		_ = json.Unmarshal(b, &respMap)
		respMap["upgrade_confirmation"] = conf
		writeJSON(w, http.StatusOK, respMap)
		return
	}

	writeJSON(w, http.StatusOK, dto)
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Forwarded-Proto, Idempotency-Key")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func normalizeProtocols(in []int) []int {
	seen := map[int]bool{}
	out := make([]int, 0, len(in))
	for _, v := range in {
		if v > 0 && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Ints(out)
	return out
}

func (s *Server) indexPage(w http.ResponseWriter, _ *http.Request) {
	content, err := ui.GetIndexHTML()
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(content)
}

func (s *Server) adminKey(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]string{"public_key": s.AdminPublicKey})
}

type deviceDTO struct {
	device.Device
	Connected bool                   `json:"connected"`
	Health    *health.HealthSnapshot `json:"health,omitempty"`
}

func (s *Server) toDeviceDTO(d device.Device) deviceDTO {
	connected := false
	if s.Broker != nil {
		connected = s.Broker.IsConnected(d.ID)
	}
	var snap *health.HealthSnapshot
	if s.Health != nil {
		if sp, err := s.Health.GetSnapshot(context.Background(), d.ID); err == nil {
			snap = sp
		}
	}
	return deviceDTO{
		Device:    d,
		Connected: connected,
		Health:    snap,
	}
}

func (s *Server) devices(w http.ResponseWriter, r *http.Request) {
	actor := auth.GetActorFromContext(r.Context())
	isOwner := actor == nil || actor.Role == auth.RoleOwner
	userID := ""
	if actor != nil {
		userID = actor.UserID
	}

	list := s.Registry.FilterDevicesForUser(userID, isOwner)
	dtos := make([]deviceDTO, 0, len(list))
	for _, d := range list {
		dtos = append(dtos, s.toDeviceDTO(d))
	}
	writeJSON(w, 200, map[string]any{"devices": dtos, "server_hash": s.serverKeySetHash(list)})
}

func (s *Server) serverKeySetHash(devices []device.Device) string {
	keys := make([]sshsync.Key, 0, len(devices)+1)
	if s.AdminPublicKey != "" {
		keys = append(keys, sshsync.Key{DeviceID: "homeagent-admin", PublicKey: s.AdminPublicKey})
	}
	for _, d := range devices {
		keys = append(keys, sshsync.Key{DeviceID: d.ID, PublicKey: d.PublicKey})
	}
	return sshsync.ComputeKeySetHash(keys)
}

func (s *Server) getDevice(w http.ResponseWriter, r *http.Request) {
	d, err := s.Registry.Get(r.PathValue("id"))
	if err != nil {
		statusError(w, err)
		return
	}
	writeJSON(w, 200, s.toDeviceDTO(d))
}

type updateDeviceReq struct {
	Alias             *string `json:"alias,omitempty"`
	MAC               *string `json:"mac,omitempty"`
	GitHubSyncEnabled *bool   `json:"github_sync_enabled,omitempty"`
}

func (s *Server) patchDevice(w http.ResponseWriter, r *http.Request) {
	devID := r.PathValue("id")
	defer r.Body.Close()
	var req updateDeviceReq
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		http.Error(w, "invalid JSON request: "+err.Error(), 400)
		return
	}

	if req.MAC != nil && strings.TrimSpace(*req.MAC) != "" {
		if _, _, err := wol.ParseAndValidateMAC(*req.MAC); err != nil {
			http.Error(w, "invalid MAC address: "+err.Error(), 400)
			return
		}
	}

	updated, err := s.Registry.UpdateDevice(devID, req.Alias, req.MAC, req.GitHubSyncEnabled)
	if err != nil {
		statusError(w, err)
		return
	}
	if s.Log != nil {
		s.Log.Info("device_properties_updated", "device_id", devID, "alias", updated.Alias, "mac", updated.MAC, "github_sync_enabled", updated.GitHubSyncEnabled)
	}

	if req.GitHubSyncEnabled != nil {
		if *req.GitHubSyncEnabled {
			if s.GitHubSyncService != nil && s.GitHubSyncService.IsConnected() && s.Broker != nil {
				_ = s.pushGitHubSync(devID)
			}
		} else {
			if s.GitHubSyncService != nil {
				_ = s.GitHubSyncService.DeleteDeviceKey(r.Context(), devID)
				_ = s.Registry.UpdateGitHubStatus(devID, "disabled", 0, "")
			}
			if s.Broker != nil {
				s.pushGitHubRevoke(devID, "sync_disabled")
			}
		}
	}

	writeJSON(w, 200, s.toDeviceDTO(updated))
}

type wakeDeviceReq struct {
	Broadcast string `json:"broadcast,omitempty"`
}

func (s *Server) wakeDevice(w http.ResponseWriter, r *http.Request) {
	devID := r.PathValue("id")
	defer r.Body.Close()

	d, err := s.Registry.Get(devID)
	if err != nil {
		statusError(w, err)
		return
	}

	// 5-second rate limiting per device
	now := time.Now()
	if lastVal, ok := s.wakeRateLimit.Load(devID); ok {
		if lastTime, ok := lastVal.(time.Time); ok && now.Sub(lastTime) < 5*time.Second {
			http.Error(w, "rate limited: please wait before sending another wake packet", http.StatusTooManyRequests)
			return
		}
	}

	var req wakeDeviceReq
	if r.ContentLength > 0 {
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			http.Error(w, "invalid JSON request: "+err.Error(), 400)
			return
		}
	}

	mac := strings.TrimSpace(d.MAC)
	if mac == "" {
		http.Error(w, "device has no MAC address configured", http.StatusBadRequest)
		return
	}

	var bcastAddrs []string
	if req.Broadcast != "" {
		cleanBcast := strings.TrimSpace(req.Broadcast)
		host := cleanBcast
		if strings.Contains(cleanBcast, ":") {
			var err error
			host, _, err = net.SplitHostPort(cleanBcast)
			if err != nil {
				http.Error(w, "invalid broadcast address format", http.StatusBadRequest)
				return
			}
		}
		ip := net.ParseIP(host)
		if ip == nil || ip.To4() == nil {
			http.Error(w, "invalid broadcast IPv4 address", http.StatusBadRequest)
			return
		}
		bcastAddrs = []string{cleanBcast}
	}

	opts := &wol.Options{
		BroadcastAddrs: bcastAddrs,
		TargetIPs:      d.Addresses,
		BurstCount:     3,
		BurstInterval:  50 * time.Millisecond,
		Port:           9,
	}

	if err := wol.Wake(mac, opts); err != nil {
		http.Error(w, "failed to send WOL packet: "+err.Error(), http.StatusInternalServerError)
		return
	}

	s.wakeRateLimit.Store(devID, now)

	if s.Log != nil {
		s.Log.Info("device_wake_sent", "device_id", d.ID, "hostname", d.Hostname, "alias", d.Alias, "mac", mac)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"message": fmt.Sprintf("WOL magic packet sent to %s", mac),
		"mac":     mac,
		"sent_at": now.UTC().Format(time.RFC3339),
	})
}

// ShutdownDeviceReq 定义关闭远程设备的请求参数。
type ShutdownDeviceReq struct {
	Reason       string `json:"reason,omitempty"`
	DelaySeconds int    `json:"delay_seconds,omitempty"`
	Force        bool   `json:"force,omitempty"`
}

func (s *Server) shutdownDevice(w http.ResponseWriter, r *http.Request) {
	devID := r.PathValue("id")
	d, err := s.Registry.Get(devID)
	if err != nil {
		statusError(w, err)
		return
	}

	var req ShutdownDeviceReq
	if r.Body != nil && r.ContentLength > 0 {
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		_ = dec.Decode(&req)
	}

	if req.Reason == "" {
		req.Reason = "api_request"
	}

	// 检查 SSE 在线连接状态
	if s.Broker != nil && !s.Broker.IsConnected(devID) {
		http.Error(w, fmt.Sprintf("device %q is offline or not connected to control plane", devID), http.StatusBadRequest)
		return
	}

	listeners := 0
	var cmd command.Command
	if s.Broker != nil {
		payload := map[string]any{
			"reason":        req.Reason,
			"delay_seconds": req.DelaySeconds,
			"force":         req.Force,
		}
		var payloadBytes []byte
		if s.Commands != nil {
			cmd, payloadBytes, err = s.prepareCommand(r, command.KindShutdown, devID, payload, s.commandTimeout(command.KindShutdown, 30*time.Second))
			if err != nil {
				commandHTTPError(w, err)
				return
			}
		} else {
			payloadBytes, _ = json.Marshal(payload)
		}
		if payloadBytes != nil {
			listeners = s.Broker.Publish(devID, broker.Event{Type: "shutdown", Data: string(payloadBytes), ID: string(cmd.ID)})
			if s.Commands != nil {
				cmd, _ = s.finishDispatch(cmd, listeners)
			}
		}
	}

	if s.Log != nil {
		s.Log.Info("device_shutdown_dispatched",
			"device_id", d.ID,
			"hostname", d.Hostname,
			"alias", d.Alias,
			"reason", req.Reason,
			"listeners", listeners,
		)
	}

	response := map[string]any{
		"device_id": d.ID,
		"status":    "shutting_down",
		"listeners": listeners,
	}
	if cmd.ID != "" {
		response["command_id"] = cmd.ID
		response["legacy_status"] = response["status"]
		response["status"] = cmd.Status
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) prepareCommand(r *http.Request, kind command.Kind, deviceID string, payload any, policy command.TimeoutPolicy) (command.Command, []byte, error) {
	wirePayload, err := json.Marshal(payload)
	if err != nil {
		return command.Command{}, nil, err
	}
	safe := wirePayload
	if kind == command.KindUpgrade {
		if p, ok := payload.(UpgradePayload); ok {
			cleanURL := p.URL
			if parsed, e := url.Parse(p.URL); e == nil {
				parsed.RawQuery = ""
				parsed.Fragment = ""
				parsed.User = nil
				cleanURL = parsed.String()
			}
			safe, _ = json.Marshal(map[string]any{"target_version": p.TargetVersion, "url": cleanURL, "sha256": p.SHA256, "force": p.Force})
		}
	}
	c, created, err := s.Commands.Create(command.CreateRequest{Kind: kind, DeviceID: deviceID, RequestedBy: requestedActor(r), IdempotencyKey: r.Header.Get("Idempotency-Key"), Request: safe, TimeoutPolicy: policy, Protocol: 1})
	if err != nil {
		return command.Command{}, nil, err
	}
	if !created {
		return c, nil, nil
	}
	c, err = s.Commands.StartDispatch(c.ID)
	if err != nil {
		return command.Command{}, nil, err
	}
	var envelope map[string]any
	if err = json.Unmarshal(wirePayload, &envelope); err != nil {
		return command.Command{}, nil, err
	}
	envelope["command_id"] = c.ID
	envelope["protocol"] = 1
	ackMode := "legacy"
	if d, e := s.Registry.Get(deviceID); e == nil && supportsControlProtocol(d, 1) {
		ackMode = "two_phase"
	}
	envelope["ack_mode"] = ackMode
	wire, err := json.Marshal(envelope)
	return c, wire, err
}
func requestedActor(r *http.Request) command.Actor {
	if session := auth.GetSessionFromContext(r.Context()); session != nil {
		return command.Actor{Type: "admin", ID: session.Username, DisplayName: session.Username}
	}
	return command.Actor{Type: "admin", ID: "legacy-admin", DisplayName: "admin"}
}
func supportsControlProtocol(d device.Device, want int) bool {
	for _, v := range d.ControlProtocols {
		if v == want {
			return true
		}
	}
	return false
}
func (s *Server) commandTimeout(kind command.Kind, finish time.Duration) command.TimeoutPolicy {
	if configured, ok := s.CommandTimeouts[kind]; ok && configured.Accept > 0 && configured.Finish > 0 {
		return configured
	}
	return command.TimeoutPolicy{Accept: 15 * time.Second, Finish: finish}
}
func (s *Server) finishDispatch(c command.Command, listeners int) (command.Command, error) {
	updated, err := s.Commands.DispatchResult(c.ID, listeners > 0)
	if err != nil {
		return updated, err
	}
	if listeners > 0 {
		if d, e := s.Registry.Get(c.DeviceID); e == nil && !supportsControlProtocol(d, 1) {
			return s.Commands.MarkLegacy(c.ID)
		}
	}
	return updated, nil
}
func commandHTTPError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	if errors.Is(err, command.ErrCommandInProgress) || errors.Is(err, command.ErrIdempotencyConflict) {
		status = http.StatusConflict
	}
	http.Error(w, err.Error(), status)
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	var d device.Device
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		http.Error(w, "invalid JSON request", 400)
		return
	}
	saved, err := s.Registry.Save(d)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if s.Log != nil {
		s.Log.Info("device_registered", "device_id", saved.ID, "hostname", saved.Hostname)
	}
	if s.Broker != nil {
		s.broadcastKeySync()
	}
	if s.Sync != nil {
		go s.Sync.SyncAll(context.Background())
	}
	writeJSON(w, 200, map[string]any{"success": true, "device_id": saved.ID})
}

func (s *Server) deleteDevice(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.GitHubSyncService != nil {
		_ = s.GitHubSyncService.DeleteDeviceKey(r.Context(), id)
	}
	if err := s.Registry.Delete(id); err != nil {
		statusError(w, err)
		return
	}
	if s.Broker != nil {
		s.Broker.CloseClient(id)
		s.broadcastKeySync()
	}
	if s.Sync != nil {
		go s.Sync.SyncAll(context.Background())
	}
	w.WriteHeader(204)
}

func (s *Server) syncDevice(w http.ResponseWriter, r *http.Request) {
	devID := r.PathValue("id")
	if _, err := s.Registry.Get(devID); err != nil {
		statusError(w, err)
		return
	}
	var cmd command.Command
	if s.Broker != nil {
		payload, err := s.resolveDeviceKeySyncPayload(devID)
		if err != nil {
			statusError(w, err)
			return
		}
		payload.Version = atomic.AddInt64(&s.version, 1)
		dataBytes, _ := json.Marshal(payload)
		if s.Commands != nil {
			cmd, dataBytes, err = s.prepareCommand(r, command.KindSSHKeys, devID, payload, s.commandTimeout(command.KindSSHKeys, time.Minute))
			if err != nil {
				commandHTTPError(w, err)
				return
			}
		}
		if dataBytes != nil {
			listeners := s.Broker.Publish(devID, broker.Event{Type: "key_sync", Data: string(dataBytes), ID: string(cmd.ID)})
			if s.Commands != nil {
				cmd, _ = s.finishDispatch(cmd, listeners)
			}
		}
	}
	response := map[string]any{"device_id": devID, "status": "ok"}
	if cmd.ID != "" {
		response["command_id"] = cmd.ID
		response["legacy_status"] = response["status"]
		response["status"] = cmd.Status
	}
	writeJSON(w, 200, response)
}

func (s *Server) syncAll(w http.ResponseWriter, r *http.Request) {
	actor := auth.GetActorFromContext(r.Context())
	isOwner := actor == nil || actor.Role == auth.RoleOwner
	userID := ""
	if actor != nil {
		userID = actor.UserID
	}
	targetDevices := s.Registry.FilterDevicesForUser(userID, isOwner)

	if s.Broker != nil && s.Commands != nil {
		commands := make([]command.Command, 0)
		version := atomic.AddInt64(&s.version, 1)
		for _, d := range targetDevices {
			payload, err := s.resolveDeviceKeySyncPayload(d.ID)
			if err != nil {
				continue
			}
			payload.Version = version
			c, data, err := s.prepareCommand(r, command.KindSSHKeys, d.ID, payload, s.commandTimeout(command.KindSSHKeys, time.Minute))
			if err != nil {
				continue
			}
			if data != nil {
				listeners := s.Broker.Publish(d.ID, broker.Event{Type: "key_sync", Data: string(data), ID: string(c.ID)})
				c, _ = s.finishDispatch(c, listeners)
			}
			commands = append(commands, c)
		}
		writeJSON(w, 200, map[string]any{"results": []any{}, "commands": commands})
		return
	} else if s.Broker != nil {
		s.broadcastKeySync()
	}
	writeJSON(w, 200, map[string]any{"results": []any{}})
}

// UpgradeRequest 定义通过 API 触发 Agent 远程升级的请求参数。
type UpgradeRequest struct {
	TargetVersion string `json:"target_version,omitempty"`
	Version       string `json:"version,omitempty"`
	URL           string `json:"url,omitempty"`
	SHA256        string `json:"sha256,omitempty"`
	Force         bool   `json:"force,omitempty"`
	Source        string `json:"source,omitempty"`
}

// UpgradePayload 表示通过 SSE 下发给 Agent 的 upgrade 事件载荷。
type UpgradePayload struct {
	Version       string `json:"version,omitempty"`
	TargetVersion string `json:"target_version,omitempty"`
	URL           string `json:"url"`
	SHA256        string `json:"sha256,omitempty"`
	Force         bool   `json:"force,omitempty"`
}

// ResolveUpgradePayload 根据目标设备的操作系统和 CPU 架构解析并填充可执行文件下载 URL、目标版本号与 SHA256 校验和。
func (s *Server) ResolveUpgradePayload(d device.Device, req UpgradeRequest, r *http.Request) (UpgradePayload, error) {

	targetVer := strings.TrimSpace(req.TargetVersion)
	if targetVer == "" {
		targetVer = strings.TrimSpace(req.Version)
	}
	if targetVer == "" {
		targetVer = version.Get()
	}

	source := strings.ToLower(strings.TrimSpace(req.Source))
	if source == "" {
		source = strings.ToLower(strings.TrimSpace(s.UpgradeSource))
	}
	if source == "" {
		source = "auto"
	}

	url := strings.TrimSpace(req.URL)
	sha := strings.ToLower(strings.TrimSpace(req.SHA256))

	if url == "" || sha == "" {
		osName := strings.ToLower(strings.TrimSpace(d.OS))
		archName := strings.ToLower(strings.TrimSpace(d.Arch))
		if osName == "" {
			osName = "linux"
		}
		if archName == "" {
			archName = "amd64"
		}

		binaryName := fmt.Sprintf("homeagent-agent-%s-%s", osName, archName)
		if osName == "windows" {
			binaryName = fmt.Sprintf("homeagent-agent-windows-%s.exe", archName)
		}

		// 1. Try local candidate files if source is "local" or "auto"
		if source == "local" || source == "auto" {
			var binaryPath string
			candidates := []string{}
			if s.DownloadsDir != "" {
				candidates = append(candidates, filepath.Join(s.DownloadsDir, binaryName))
			}
			candidates = append(candidates,
				filepath.Join("dist", binaryName),
				filepath.Join("bin", binaryName),
			)

			for _, cand := range candidates {
				if info, err := os.Stat(cand); err == nil && !info.IsDir() {
					binaryPath = cand
					break
				}
			}

			if binaryPath != "" {
				if sha == "" {
					b, err := os.ReadFile(binaryPath)
					if err == nil {
						sum := sha256.Sum256(b)
						sha = hex.EncodeToString(sum[:])
					}
				}
				if url == "" && r != nil {
					scheme := "http"
					if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
						scheme = "https"
					}
					host := resolveServerHost(r)
					url = fmt.Sprintf("%s://%s/downloads/%s", scheme, host, binaryName)
				}
			}
		}

		// 2. If still unpopulated and source is "github" or "auto", resolve from GitHub Releases
		if (url == "" || sha == "") && source != "local" {
			ghClient := s.GitHubReleaseClient
			if ghClient == nil {
				ghClient = githubrelease.NewClient(githubrelease.Config{
					Repo:         s.GitHubRepo,
					MirrorPrefix: s.GitHubMirrorPrefix,
				})
			}
			if url == "" {
				url = ghClient.BuildAssetDownloadURL(targetVer, binaryName)
			}
			if sha == "" {
				ctx := context.Background()
				if r != nil {
					ctx = r.Context()
				}
				var err error
				sha, err = ghClient.FetchAssetSHA256(ctx, targetVer, binaryName)
				if err != nil {
					return UpgradePayload{}, fmt.Errorf("failed to fetch sha256 for %s (%s) from github: %w", binaryName, targetVer, err)
				}
			}
		}
	}

	if url == "" {
		return UpgradePayload{}, fmt.Errorf("could not determine download URL for device %s (%s/%s)", d.ID, d.OS, d.Arch)
	}

	return UpgradePayload{
		TargetVersion: targetVer,
		Version:       targetVer,
		URL:           url,
		SHA256:        sha,
		Force:         req.Force,
	}, nil
}

func (s *Server) upgradeDevice(w http.ResponseWriter, r *http.Request) {
	deviceID := r.PathValue("id")
	if deviceID == "all" {
		s.upgradeAll(w, r)
		return
	}

	d, err := s.Registry.Get(deviceID)
	if err != nil {
		statusError(w, err)
		return
	}

	if d.UpgradeSecurityMode == "v2_locked" && !s.MacOSAppUpgradeV2Enabled {
		http.Error(w, "v2_upgrade_temporarily_unavailable", http.StatusBadRequest)
		return
	}

	var req UpgradeRequest
	if r.Body != nil && r.ContentLength > 0 {
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		_ = dec.Decode(&req)
	}

	payload, err := s.ResolveUpgradePayload(d, req, r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var plan *upgradeplan.UpgradePlan
	if s.UpgradePlans != nil {
		idemKey := r.Header.Get("Idempotency-Key")
		actor := auth.GetActorFromContext(r.Context())
		actorObj := command.Actor{Type: "anonymous", ID: "anonymous"}
		if actor != nil {
			actorObj = command.Actor{Type: "user", ID: actor.UserID, DisplayName: actor.Username}
		}

		reqDigestSum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%s|%v", payload.TargetVersion, payload.URL, payload.SHA256, payload.Force)))
		reqDigest := hex.EncodeToString(reqDigestSum[:])

		var bridgeVerPtr *string
		if len(d.ControlProtocols) < 2 {
			bv := payload.TargetVersion
			bridgeVerPtr = &bv
		}

		p, _, pErr := s.UpgradePlans.CreatePlan(upgradeplan.CreatePlanRequest{
			DeviceID:       deviceID,
			RequestedBy:    actorObj,
			IdempotencyKey: idemKey,
			TargetVersion:  payload.TargetVersion,
			BridgeVersion:  bridgeVerPtr,
			Snapshot: upgradeplan.PlanSnapshot{
				TargetVersion:       payload.TargetVersion,
				TargetURL:           payload.URL,
				TargetSHA256:        payload.SHA256,
				InitialSecurityMode: d.UpgradeSecurityMode,
				InitialProtocols:    d.ControlProtocols,
				RequestDigest:       reqDigest,
			},
		})
		if errors.Is(pErr, upgradeplan.ErrIdempotencyConflict) {
			http.Error(w, "idempotency conflict on upgrade plan", http.StatusConflict)
			return
		} else if errors.Is(pErr, upgradeplan.ErrPlanInProgress) {
			http.Error(w, "upgrade plan already in progress for device", http.StatusConflict)
			return
		}
		plan = p
	}

	listeners := 0
	var cmd command.Command
	if s.Broker != nil {
		var dataBytes []byte
		if s.Commands != nil {
			cmd, dataBytes, err = s.prepareCommand(r, command.KindUpgrade, deviceID, payload, s.commandTimeout(command.KindUpgrade, 10*time.Minute))
			if err != nil {
				commandHTTPError(w, err)
				return
			}
		} else {
			dataBytes, _ = json.Marshal(payload)
		}
		if dataBytes != nil {
			listeners = s.Broker.Publish(deviceID, broker.Event{Type: "upgrade", Data: string(dataBytes), ID: string(cmd.ID)})
			if s.Commands != nil {
				cmd, _ = s.finishDispatch(cmd, listeners)
			}
		}
	}

	if s.Log != nil {
		s.Log.Info("upgrade_instruction_dispatched",
			"device_id", deviceID,
			"target_version", payload.TargetVersion,
			"url", payload.URL,
			"listeners", listeners,
		)
	}

	response := map[string]any{
		"device_id":        deviceID,
		"target_version":   payload.TargetVersion,
		"url":              payload.URL,
		"sha256":           payload.SHA256,
		"active_listeners": listeners,
		"status":           "ok",
	}
	if cmd.ID != "" {
		response["command_id"] = cmd.ID
		response["legacy_status"] = response["status"]
		response["status"] = cmd.Status
	}
	if plan != nil {
		response["plan_id"] = plan.PlanID
		response["plan_stage"] = plan.Stage
		if plan.BridgeCommandID != nil {
			response["bridge_command_id"] = *plan.BridgeCommandID
		}
		if plan.TargetCommandID != nil {
			response["target_command_id"] = *plan.TargetCommandID
		}
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) listUpgradePlans(w http.ResponseWriter, r *http.Request) {
	if s.UpgradePlans == nil {
		writeJSON(w, http.StatusOK, []upgradeplan.UpgradePlan{})
		return
	}
	deviceID := r.URL.Query().Get("device_id")
	stage := upgradeplan.PlanStage(r.URL.Query().Get("stage"))
	plans, err := s.UpgradePlans.ListPlans(upgradeplan.Filter{
		DeviceID: deviceID,
		Stage:    stage,
	})
	if err != nil {
		statusError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, plans)
}

func (s *Server) getUpgradePlan(w http.ResponseWriter, r *http.Request) {
	if s.UpgradePlans == nil {
		http.Error(w, "upgrade plans not supported", http.StatusNotFound)
		return
	}
	planID := r.PathValue("id")
	p, err := s.UpgradePlans.GetPlan(planID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) upgradeAll(w http.ResponseWriter, r *http.Request) {
	var req UpgradeRequest
	if r.Body != nil && r.ContentLength > 0 {
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		_ = dec.Decode(&req)
	}

	actor := auth.GetActorFromContext(r.Context())
	isOwner := actor == nil || actor.Role == auth.RoleOwner
	userID := ""
	if actor != nil {
		userID = actor.UserID
	}
	devices := s.Registry.FilterDevicesForUser(userID, isOwner)
	targetVer := strings.TrimSpace(req.TargetVersion)
	if targetVer == "" {
		targetVer = strings.TrimSpace(req.Version)
	}
	if targetVer == "" {
		targetVer = version.Get()
	}

	var triggered []string
	commands := make([]command.Command, 0)
	type deviceResult struct {
		DeviceID  string `json:"device_id"`
		Status    string `json:"status"`
		Reason    string `json:"reason,omitempty"`
		Message   string `json:"message,omitempty"`
		CommandID string `json:"command_id,omitempty"`
	}
	deviceResults := make([]deviceResult, 0, len(devices))
	skippedCount := 0
	failedCount := 0
	for _, d := range devices {
		if s.Broker == nil {
			failedCount++
			deviceResults = append(deviceResults, deviceResult{DeviceID: d.ID, Status: "failed", Reason: "broker_unavailable"})
			continue
		}
		if !s.Broker.IsConnected(d.ID) {
			skippedCount++
			deviceResults = append(deviceResults, deviceResult{DeviceID: d.ID, Status: "skipped", Reason: "device_offline"})
			continue
		}
		payload, err := s.ResolveUpgradePayload(d, req, r)
		if err != nil {
			failedCount++
			deviceResults = append(deviceResults, deviceResult{DeviceID: d.ID, Status: "failed", Reason: "artifact_unavailable", Message: err.Error()})
			continue
		}
		dataBytes, _ := json.Marshal(payload)
		var cmd command.Command
		if s.Commands != nil {
			cmd, dataBytes, err = s.prepareCommand(r, command.KindUpgrade, d.ID, payload, s.commandTimeout(command.KindUpgrade, 10*time.Minute))
			if err != nil {
				failedCount++
				deviceResults = append(deviceResults, deviceResult{DeviceID: d.ID, Status: "failed", Reason: "command_rejected", Message: err.Error()})
				continue
			}
			commands = append(commands, cmd)
		}
		count := 0
		if dataBytes != nil {
			count = s.Broker.Publish(d.ID, broker.Event{Type: "upgrade", Data: string(dataBytes), ID: string(cmd.ID)})
			if s.Commands != nil {
				cmd, _ = s.finishDispatch(cmd, count)
				commands[len(commands)-1] = cmd
			}
		}
		if count == 0 {
			failedCount++
			deviceResults = append(deviceResults, deviceResult{DeviceID: d.ID, Status: "failed", Reason: "no_active_listener"})
			continue
		}
		triggered = append(triggered, d.ID)
		deviceResults = append(deviceResults, deviceResult{DeviceID: d.ID, Status: "dispatched", CommandID: string(cmd.ID)})
	}

	if triggered == nil {
		triggered = []string{}
	}

	if s.Log != nil {
		s.Log.Info("upgrade_all_dispatched",
			"target_version", targetVer,
			"count", len(triggered),
		)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"target_version":   targetVer,
		"triggered":        triggered,
		"total":            len(triggered),
		"dispatched_count": len(triggered),
		"skipped_count":    skippedCount,
		"failed_count":     failedCount,
		"device_results":   deviceResults,
		"status":           "ok",
		"commands":         commands,
	})
}

// SSE streaming endpoint: GET /api/v1/devices/{id}/events
func (s *Server) deviceEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", 500)
		return
	}
	if s.Broker == nil {
		http.Error(w, "broker disabled", 503)
		return
	}

	deviceID := r.PathValue("id")
	if _, err := s.Registry.Get(deviceID); err != nil {
		statusError(w, err)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ch, unsubscribe := s.Broker.Subscribe(deviceID)
	defer unsubscribe()

	// 1. Immediately send full snapshot to newly connected device
	payload, err := s.resolveDeviceKeySyncPayload(deviceID)
	if err == nil {
		dataBytes, _ := json.Marshal(payload)
		fmt.Fprintf(w, "event: key_sync\ndata: %s\n\n", dataBytes)
		flusher.Flush()
	}

	// 2. If device has GitHub sync enabled and GitHub is connected, send github_credentials_sync
	if s.GitHubSyncService != nil && s.GitHubSyncService.IsConnected() {
		if d, err := s.Registry.Get(deviceID); err == nil && d.GitHubSyncEnabled {
			if ghPayload, err := s.GitHubSyncService.ResolveSyncPayload(); err == nil {
				dataBytes, _ := json.Marshal(ghPayload)
				fmt.Fprintf(w, "event: github_credentials_sync\ndata: %s\n\n", dataBytes)
				flusher.Flush()
			}
		}
	}

	pingInterval := s.PingInterval
	if pingInterval <= 0 {
		pingInterval = 30 * time.Second
	}
	pingTicker := time.NewTicker(pingInterval)
	defer pingTicker.Stop()

	if s.Log != nil {
		s.Log.Info("sse_client_connected", "device_id", deviceID)
	}

	for {
		select {
		case <-r.Context().Done():
			if s.Log != nil {
				s.Log.Info("sse_client_disconnected", "device_id", deviceID)
			}
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if ev.Type != "" {
				fmt.Fprintf(w, "event: %s\n", ev.Type)
			}
			if ev.ID != "" {
				fmt.Fprintf(w, "id: %s\n", ev.ID)
			}
			fmt.Fprintf(w, "data: %s\n\n", ev.Data)
			flusher.Flush()
		case t := <-pingTicker.C:
			pingData, _ := json.Marshal(map[string]int64{"timestamp": t.Unix()})
			fmt.Fprintf(w, "event: ping\ndata: %s\n\n", pingData)
			flusher.Flush()
		}
	}
}

// AckRequest 表示 Agent 守护进程在应用配置或自升级后向服务端发送的状态确认回执（ACK）。
type AckRequest struct {
	CommandID            command.ID      `json:"command_id,omitempty"`
	Protocol             int             `json:"protocol,omitempty"`
	AckMode              string          `json:"ack_mode,omitempty"`
	Module               string          `json:"module"`
	Status               string          `json:"status"`
	AppliedVersion       int64           `json:"applied_version"`
	AppliedHash          string          `json:"applied_hash"`
	ErrorMessage         string          `json:"error_message"`
	AgentVersion         string          `json:"agent_version,omitempty"`
	SSHFingerprint       string          `json:"ssh_fingerprint,omitempty"`
	GitHubVersion        int64           `json:"github_version,omitempty"`
	Result               json.RawMessage `json:"result,omitempty"`
	ErrorCode            string          `json:"error_code,omitempty"`
	Sequence             *uint64         `json:"sequence,omitempty"`
	Phase                string          `json:"phase,omitempty"`
	OccurredAt           *int64          `json:"occurred_at,omitempty"`
	PhaseResult          json.RawMessage `json:"phase_result,omitempty"`
	UpgradeTransactionID string          `json:"upgrade_transaction_id,omitempty"`
}

// 客户端状态 ACK 上报端点：POST /api/v1/devices/{id}/ack
func (s *Server) deviceAck(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	deviceID := r.PathValue("id")
	var req AckRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		http.Error(w, "invalid JSON request", 400)
		return
	}
	var commandForProjection *command.Command
	if req.CommandID != "" && s.Commands != nil {
		if req.Status != "accepted" && req.Status != "succeeded" && req.Status != "failed" && req.Status != "error" && req.Status != "progress" && req.Status != "synced" && req.Status != "upgraded" {
			http.Error(w, "invalid ACK status", http.StatusBadRequest)
			return
		}
		c, err := s.Commands.Get(req.CommandID)
		if err != nil || c.DeviceID != deviceID {
			http.Error(w, "command not found", http.StatusNotFound)
			return
		}
		expected := commandKindForModule(req.Module)
		githubCompatible := req.Module == "github_credentials" && (c.Kind == command.KindGitHubSync || c.Kind == command.KindGitHubRevoke)
		if expected == "" || (expected != c.Kind && !githubCompatible) {
			http.Error(w, "command kind mismatch", http.StatusConflict)
			return
		}
		if req.Status == "accepted" {
			if _, err = s.Commands.Accept(req.CommandID); err != nil {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"success": true})
			return
		}
		if req.Status == "progress" {
			if req.Module != "upgrade" {
				http.Error(w, "status=progress only allowed for upgrade module", http.StatusBadRequest)
				return
			}
			var seq uint64
			if req.Sequence != nil {
				seq = *req.Sequence
			}
			var occ time.Time
			if req.OccurredAt != nil {
				occ = time.UnixMilli(*req.OccurredAt).UTC()
			} else {
				occ = time.Now().UTC()
			}
			p := command.UpgradeProgress{
				Phase:        req.Phase,
				Sequence:     seq,
				OccurredAt:   occ,
				ErrorMessage: req.ErrorMessage,
			}
			_, _ = s.Commands.UpdateProgress(req.CommandID, p)

			if req.Phase == "commit_ready" {
				var subResult struct {
					FenceDigest string `json:"fence_digest"`
					FactsDigest string `json:"facts_digest"`
					ServerNonce string `json:"server_nonce"`
				}
				if len(req.PhaseResult) > 0 {
					_ = json.Unmarshal(req.PhaseResult, &subResult)
				}
				_, _ = s.Commands.Finish(req.CommandID, command.StatusSucceeded, json.RawMessage(`{"status":"converged"}`), "", "")
				if s.UpgradePlans != nil {
					if activePlan, pErr := s.UpgradePlans.GetActivePlanByDevice(deviceID); pErr == nil {
						_, _ = s.UpgradePlans.TransitionStage(activePlan.PlanID, activePlan.Revision, upgradeplan.StageSucceeded, "")
					}
				}
				conf := &upgrade.UpgradeConfirmation{
					State:         "committed",
					CommandID:     string(req.CommandID),
					FenceRevision: 0,
					FenceDigest:   subResult.FenceDigest,
					FactsDigest:   subResult.FactsDigest,
					ServerNonce:   subResult.ServerNonce,
				}
				writeJSON(w, http.StatusOK, map[string]any{"success": true, "upgrade_confirmation": conf})
				return
			}

			writeJSON(w, http.StatusOK, map[string]any{"success": true})
			return
		}

		status := command.StatusSucceeded
		if req.Status == "failed" || req.Status == "error" {
			status = command.StatusFailed
		}
		result := resultForAck(c.Kind, req, status)
		finished, finishErr := s.Commands.Finish(req.CommandID, status, result, req.ErrorCode, req.ErrorMessage)
		err = finishErr
		if errors.Is(err, command.ErrLateAckAccepted) {
			writeJSON(w, http.StatusAccepted, map[string]any{"success": true, "status": "accepted_for_audit"})
			return
		} else if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		commandForProjection = &finished
	}

	var projectionErr error
	switch req.Module {
	case "github_credentials":
		ghStatus := req.Status
		if ghStatus == "succeeded" {
			ghStatus = "synced"
		}
		projectionErr = s.Registry.UpdateGitHubStatus(deviceID, ghStatus, 0, req.SSHFingerprint)
	case "upgrade":
		// upgrade ACK: Do NOT overwrite SSH key sync status (AppliedVersion/AppliedHash).
		// AgentVersion is a runtime fact and is updated only by a subsequent facts report.
		projectionErr = s.Registry.TouchLastSeen(deviceID)
	case "shutdown":
		// shutdown ACK: Do NOT overwrite SSH key sync status.
		projectionErr = s.Registry.TouchLastSeen(deviceID)
	default:
		// Default to handling ssh_keys module (or legacy ACK without explicit module)
		syncStatus := req.Status
		if syncStatus == "succeeded" {
			syncStatus = "synced"
		}
		if err := s.Registry.UpdateSyncStatus(deviceID, syncStatus, req.AppliedVersion, req.AppliedHash, req.ErrorMessage); err != nil {
			statusError(w, err)
			return
		}
	}
	if commandForProjection != nil && commandForProjection.Projection.Status == "pending" {
		if projectionErr != nil {
			_, _ = s.Commands.MarkProjection(req.CommandID, "failed", projectionErr.Error())
		} else {
			_, _ = s.Commands.MarkProjection(req.CommandID, "applied", "")
		}
	}

	if s.Log != nil {
		s.Log.Info("device_ack_received",
			"device_id", deviceID,
			"module", req.Module,
			"status", req.Status,
			"applied_version", req.AppliedVersion,
			"applied_hash", req.AppliedHash,
			"agent_version", req.AgentVersion,
			"ssh_fingerprint", req.SSHFingerprint,
			"error", req.ErrorMessage,
		)
	}

	// Self-healing reconcile: check if client hash matches expected server hash
	if req.Status == "synced" && req.AppliedHash != "" {
		if req.Module == "ssh_keys" {
			expected, err := s.resolveDeviceKeySyncPayload(deviceID)
			if err == nil && req.AppliedHash != expected.Hash && s.Broker != nil {
				if s.Log != nil {
					s.Log.Warn("device_hash_mismatch_triggering_resync",
						"device_id", deviceID,
						"reported_hash", req.AppliedHash,
						"expected_hash", expected.Hash,
					)
				}
				dataBytes, _ := json.Marshal(expected)
				s.Broker.Publish(deviceID, broker.Event{
					Type: "key_sync",
					Data: string(dataBytes),
				})
			}
		} else if req.Module == "github_credentials" && s.GitHubSyncService != nil && s.GitHubSyncService.IsConnected() {
			expected, err := s.GitHubSyncService.ResolveSyncPayload()
			if err == nil && req.AppliedHash != expected.Hash && s.Broker != nil {
				if s.Log != nil {
					s.Log.Warn("device_github_hash_mismatch_triggering_resync",
						"device_id", deviceID,
						"reported_hash", req.AppliedHash,
						"expected_hash", expected.Hash,
					)
				}
				_ = s.pushGitHubSync(deviceID)
			}
		}
	}

	writeJSON(w, 200, map[string]any{"success": true})
}

func resultForAck(kind command.Kind, req AckRequest, status command.Status) json.RawMessage {
	var value any
	switch kind {
	case command.KindSSHKeys:
		v := struct {
			AppliedVersion int64  `json:"applied_version"`
			AppliedHash    string `json:"applied_hash"`
			KeyCount       int    `json:"key_count"`
		}{AppliedVersion: req.AppliedVersion, AppliedHash: req.AppliedHash}
		decodeStrictResult(req.Result, &v)
		value = v
	case command.KindUpgrade:
		v := struct {
			PreviousVersion  string `json:"previous_version"`
			TargetVersion    string `json:"target_version"`
			BinaryReplaced   bool   `json:"binary_replaced"`
			RestartScheduled bool   `json:"restart_scheduled"`
		}{BinaryReplaced: status == command.StatusSucceeded, RestartScheduled: status == command.StatusSucceeded}
		decodeStrictResult(req.Result, &v)
		value = v
	case command.KindShutdown:
		v := struct {
			OSCommandStarted      bool `json:"os_command_started"`
			ScheduledDelaySeconds int  `json:"scheduled_delay_seconds"`
		}{OSCommandStarted: status == command.StatusSucceeded}
		decodeStrictResult(req.Result, &v)
		value = v
	case command.KindGitHubSync:
		v := struct {
			Version        int64  `json:"version"`
			Hash           string `json:"hash"`
			SSHFingerprint string `json:"ssh_fingerprint"`
		}{req.GitHubVersion, req.AppliedHash, req.SSHFingerprint}
		decodeStrictResult(req.Result, &v)
		value = v
	case command.KindGitHubRevoke:
		v := struct {
			CredentialsRemoved bool `json:"credentials_removed"`
			ConfigRemoved      bool `json:"config_removed"`
		}{status == command.StatusSucceeded, status == command.StatusSucceeded}
		decodeStrictResult(req.Result, &v)
		value = v
	}
	b, _ := json.Marshal(value)
	return b
}

func decodeStrictResult(raw json.RawMessage, dst any) bool {
	if len(raw) == 0 {
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	return dec.Decode(dst) == nil
}

func (s *Server) recoverCommandProjections() {
	items, err := s.Commands.ProjectionPending(1000)
	if err != nil {
		return
	}
	for _, c := range items {
		projectionErr := s.applyCommandProjection(c)
		if projectionErr != nil {
			_, _ = s.Commands.MarkProjection(c.ID, "failed", projectionErr.Error())
		} else {
			_, _ = s.Commands.MarkProjection(c.ID, "applied", "")
		}
	}
}
func (s *Server) applyCommandProjection(c command.Command) error {
	status := "synced"
	if c.Status == command.StatusFailed {
		status = "error"
	}
	switch c.Kind {
	case command.KindSSHKeys:
		var v struct {
			AppliedVersion int64  `json:"applied_version"`
			AppliedHash    string `json:"applied_hash"`
		}
		if err := json.Unmarshal(c.Result, &v); err != nil {
			return err
		}
		return s.Registry.UpdateSyncStatus(c.DeviceID, status, v.AppliedVersion, v.AppliedHash, c.ErrorMessage)
	case command.KindUpgrade, command.KindShutdown:
		return s.Registry.TouchLastSeen(c.DeviceID)
	case command.KindGitHubSync:
		var v struct {
			SSHFingerprint string `json:"ssh_fingerprint"`
		}
		if err := json.Unmarshal(c.Result, &v); err != nil {
			return err
		}
		return s.Registry.UpdateGitHubStatus(c.DeviceID, status, 0, v.SSHFingerprint)
	case command.KindGitHubRevoke:
		return s.Registry.UpdateGitHubStatus(c.DeviceID, status, 0, "")
	}
	return nil
}

func commandKindForModule(module string) command.Kind {
	switch module {
	case "ssh_keys":
		return command.KindSSHKeys
	case "upgrade":
		return command.KindUpgrade
	case "shutdown":
		return command.KindShutdown
	case "github_credentials", "github_credentials_sync":
		return command.KindGitHubSync
	case "github_credentials_revoke":
		return command.KindGitHubRevoke
	}
	return ""
}

func (s *Server) listCommands(w http.ResponseWriter, r *http.Request) {
	if s.Commands == nil {
		http.Error(w, "command service unavailable", http.StatusServiceUnavailable)
		return
	}

	actor := auth.GetActorFromContext(r.Context())
	isOwner := actor == nil || actor.Role == auth.RoleOwner
	reqDevID := r.URL.Query().Get("device_id")

	if !isOwner && s.Registry != nil && reqDevID != "" {
		if visible, exists := s.Registry.IsDeviceVisible(actor.UserID, reqDevID); !visible || !exists {
			writeJSON(w, 200, command.Page{Commands: []command.Command{}})
			return
		}
	}

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	p, err := s.Commands.List(command.Filter{DeviceID: reqDevID, Kind: command.Kind(r.URL.Query().Get("kind")), Status: command.Status(r.URL.Query().Get("status")), Limit: limit, Cursor: r.URL.Query().Get("cursor")})
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	if !isOwner && s.Registry != nil {
		visibleDevs := s.Registry.FilterDevicesForUser(actor.UserID, false)
		visibleMap := make(map[string]bool)
		for _, d := range visibleDevs {
			visibleMap[d.ID] = true
		}
		var filtered []command.Command
		for _, cmd := range p.Commands {
			if visibleMap[cmd.DeviceID] {
				cmd.Request = nil
				filtered = append(filtered, cmd)
			}
		}
		p.Commands = filtered
	} else {
		for i := range p.Commands {
			p.Commands[i].Request = nil
		}
	}

	writeJSON(w, 200, p)
}

func (s *Server) getCommand(w http.ResponseWriter, r *http.Request) {
	if s.Commands == nil {
		http.Error(w, "command service unavailable", http.StatusServiceUnavailable)
		return
	}
	c, err := s.Commands.Get(command.ID(r.PathValue("id")))
	if err != nil {
		http.Error(w, "command not found", 404)
		return
	}

	actor := auth.GetActorFromContext(r.Context())
	if actor != nil && actor.Role != auth.RoleOwner && s.Registry != nil {
		if visible, exists := s.Registry.IsDeviceVisible(actor.UserID, c.DeviceID); !visible || !exists {
			http.Error(w, "command not found", 404)
			return
		}
	}

	writeJSON(w, 200, c)
}

func (s *Server) cancelCommand(w http.ResponseWriter, r *http.Request) {
	if s.Commands == nil {
		http.Error(w, "command service unavailable", http.StatusServiceUnavailable)
		return
	}

	c, err := s.Commands.Get(command.ID(r.PathValue("id")))
	if err != nil {
		http.Error(w, "command not found", 404)
		return
	}

	actor := auth.GetActorFromContext(r.Context())
	if actor != nil && actor.Role != auth.RoleOwner && s.Registry != nil {
		if visible, exists := s.Registry.IsDeviceVisible(actor.UserID, c.DeviceID); !visible || !exists {
			http.Error(w, "command not found", 404)
			return
		}
	}

	cancelled, err := s.Commands.Cancel(c.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, 200, cancelled)
}

// Pull keys fallback endpoint: GET /api/v1/devices/{id}/keys
func (s *Server) deviceKeys(w http.ResponseWriter, r *http.Request) {
	deviceID := r.PathValue("id")
	payload, err := s.resolveDeviceKeySyncPayload(deviceID)
	if err != nil {
		statusError(w, err)
		return
	}
	writeJSON(w, 200, payload)
}

func (s *Server) resolveDeviceKeySyncPayload(targetID string) (sshsync.KeySyncPayload, error) {
	_, err := s.Registry.Get(targetID)
	if err != nil {
		return sshsync.KeySyncPayload{}, err
	}

	policy, _ := acl.Load(s.ACLPath)
	all := s.Registry.List()
	allowed := policy.Resolve(targetID, all)

	var keys []sshsync.Key
	if s.AdminPublicKey != "" {
		keys = append(keys, sshsync.Key{DeviceID: "homeagent-admin", PublicKey: s.AdminPublicKey})
	}
	for _, d := range allowed {
		keys = append(keys, sshsync.Key{DeviceID: d.ID, PublicKey: d.PublicKey})
	}

	version := atomic.LoadInt64(&s.version)
	if version == 0 {
		version = 1
	}
	hash := sshsync.ComputeKeySetHash(keys)

	return sshsync.KeySyncPayload{
		Version: version,
		Hash:    hash,
		Keys:    keys,
	}, nil
}

func (s *Server) pushKeySync(deviceID string) error {
	payload, err := s.resolveDeviceKeySyncPayload(deviceID)
	if err != nil {
		return err
	}
	newVersion := atomic.AddInt64(&s.version, 1)
	payload.Version = newVersion
	dataBytes, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	s.Broker.Publish(deviceID, broker.Event{
		Type: "key_sync",
		Data: string(dataBytes),
	})
	return nil
}

func (s *Server) broadcastKeySync() {
	newVersion := atomic.AddInt64(&s.version, 1)
	active := s.Broker.ActiveDevices()
	for _, devID := range active {
		payload, err := s.resolveDeviceKeySyncPayload(devID)
		if err != nil {
			continue
		}
		payload.Version = newVersion
		dataBytes, err := json.Marshal(payload)
		if err != nil {
			continue
		}
		s.Broker.Publish(devID, broker.Event{
			Type: "key_sync",
			Data: string(dataBytes),
		})
	}
}

func statusError(w http.ResponseWriter, err error) {
	if errors.Is(err, registry.ErrNotFound) {
		http.Error(w, "not found", 404)
	} else {
		http.Error(w, "internal error", 500)
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

// RedactedRequest 格式化 HTTP 请求信息并自动对 Authorization 头中的令牌脱敏，用于安全日志输出。
func RedactedRequest(r *http.Request) string {

	return r.Method + " " + r.URL.Path + " authorization=" + strings.Repeat("*", len(r.Header.Get("Authorization")))
}

type deviceNetworkStateReq struct {
	NetworkID     string                            `json:"network_id"`
	Revision      uint64                            `json:"revision"`
	ObservedAt    time.Time                         `json:"observed_at"`
	IPv6Addresses []networkaddr.ReportedIPv6Address `json:"ipv6_addresses"`
	MAC           string                            `json:"mac,omitempty"`
}

func (s *Server) putDeviceNetworkState(w http.ResponseWriter, r *http.Request) {
	if s.DeviceStateService == nil {
		http.Error(w, "device state service not configured", http.StatusNotImplemented)
		return
	}
	devID := r.PathValue("id")
	if strings.TrimSpace(devID) == "" {
		http.Error(w, "missing device id", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()
	var req deviceNetworkStateReq
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		http.Error(w, "invalid JSON request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.ObservedAt.IsZero() {
		req.ObservedAt = time.Now().UTC()
	}

	st, changed, err := s.DeviceStateService.UpdateReportedAddresses(
		devID, req.NetworkID, req.Revision, req.ObservedAt, req.IPv6Addresses,
	)
	if err != nil {
		var conflict *devicestate.RevisionConflictError
		if errors.As(err, &conflict) {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error":             string(conflict.Kind),
				"current_revision":  conflict.Current,
				"received_revision": conflict.Received,
			})
			return
		}
		if errors.Is(err, devicestate.ErrRevisionConflict) || errors.Is(err, devicestate.ErrRevisionContentMismatch) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if s.Registry != nil && strings.TrimSpace(req.MAC) != "" {
		if existing, err := s.Registry.Get(devID); err == nil {
			if existing.MAC != req.MAC {
				_, _ = s.Registry.UpdateMAC(devID, req.MAC)
			}
		}
	}

	// Calculate and update desired address against router prefixes
	if s.PrefixStateService != nil {
		activePrefixes, isStale, _ := s.PrefixStateService.GetActivePrefixes(req.NetworkID, time.Now().UTC(), 15*time.Minute)
		if !isStale && len(activePrefixes) > 0 {
			validCandidates := prefixstate.Intersect(st.ReportedAddresses, activePrefixes)
			if desiredIP, ok := prefixstate.SelectAddress(validCandidates, st.AppliedAddress); ok {
				st.DesiredAddress = desiredIP.String()
			} else {
				st.DesiredAddress = ""
			}
			_ = s.DeviceStateService.Save(*st)
		} else if len(st.ReportedAddresses) > 0 && st.DesiredAddress == "" {
			st.DesiredAddress = st.ReportedAddresses[0].Address
			_ = s.DeviceStateService.Save(*st)
		}
	} else if len(st.ReportedAddresses) > 0 && st.DesiredAddress == "" {
		st.DesiredAddress = st.ReportedAddresses[0].Address
		_ = s.DeviceStateService.Save(*st)
	}

	if changed && s.DDNSService != nil {
		go func() {
			_ = s.DDNSService.ReconcileDevice(context.Background(), devID)
		}()
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"accepted_revision": st.Revision,
		"changed":           changed,
		"sync_status":       st.SyncStatus,
		"desired_address":   st.DesiredAddress,
	})
}

func (s *Server) getDeviceNetworkState(w http.ResponseWriter, r *http.Request) {
	if s.DeviceStateService == nil {
		http.Error(w, "device state service not configured", http.StatusNotImplemented)
		return
	}
	devID := r.PathValue("id")
	st, err := s.DeviceStateService.Get(devID)
	if err != nil {
		if errors.Is(err, devicestate.ErrDeviceNotFound) {
			http.Error(w, "device network state not found", http.StatusNotFound)
			return
		}
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) getDeviceIPv6Text(w http.ResponseWriter, r *http.Request) {
	if s.DeviceStateService == nil {
		http.Error(w, "device state service not configured", http.StatusNotImplemented)
		return
	}
	devID := r.PathValue("id")
	st, err := s.DeviceStateService.Get(devID)
	if err != nil {
		if errors.Is(err, devicestate.ErrDeviceNotFound) {
			http.Error(w, "device not found", http.StatusNotFound)
			return
		}
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	ip := strings.TrimSpace(st.DesiredAddress)
	if ip == "" && len(st.ReportedAddresses) > 0 {
		ip = st.ReportedAddresses[0].Address
	}

	if ip == "" {
		http.Error(w, "no valid IPv6 address found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(ip + "\n"))
}

type routerPrefixesReq struct {
	NetworkID  string                           `json:"network_id"`
	Revision   uint64                           `json:"revision"`
	ObservedAt time.Time                        `json:"observed_at"`
	Prefixes   []prefixstate.ReportedIPv6Prefix `json:"prefixes"`
	MAC        string                           `json:"mac,omitempty"`
}

func (s *Server) putRouterPrefixes(w http.ResponseWriter, r *http.Request) {
	if s.PrefixStateService == nil {
		http.Error(w, "prefix state service not configured", http.StatusNotImplemented)
		return
	}
	routerDevID := r.PathValue("id")
	if strings.TrimSpace(routerDevID) == "" {
		http.Error(w, "missing router device id", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()
	var req routerPrefixesReq
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		http.Error(w, "invalid JSON request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.NetworkID == "" {
		http.Error(w, "network_id is required", http.StatusBadRequest)
		return
	}
	if req.ObservedAt.IsZero() {
		req.ObservedAt = time.Now().UTC()
	}

	st, changed, err := s.PrefixStateService.UpdateRouterPrefixes(
		routerDevID, req.NetworkID, req.Revision, req.ObservedAt, req.Prefixes,
	)
	if err != nil {
		var conflict *prefixstate.RevisionConflictError
		if errors.As(err, &conflict) {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error":             string(conflict.Kind),
				"current_revision":  conflict.Current,
				"received_revision": conflict.Received,
			})
			return
		}
		if errors.Is(err, prefixstate.ErrRevisionConflict) || errors.Is(err, prefixstate.ErrRevisionContentMismatch) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if s.Registry != nil && strings.TrimSpace(req.MAC) != "" {
		if existing, err := s.Registry.Get(routerDevID); err == nil {
			if existing.MAC != req.MAC {
				_, _ = s.Registry.UpdateMAC(routerDevID, req.MAC)
			}
		}
	}

	// Recalculate desired address for all devices in this network
	if s.DeviceStateService != nil {
		allDevs, _ := s.DeviceStateService.List()
		activePrefixes, isStale, _ := s.PrefixStateService.GetActivePrefixes(req.NetworkID, time.Now().UTC(), 15*time.Minute)
		for _, dev := range allDevs {
			if dev.NetworkID == req.NetworkID {
				if !isStale && len(activePrefixes) > 0 {
					validCandidates := prefixstate.Intersect(dev.ReportedAddresses, activePrefixes)
					if desiredIP, ok := prefixstate.SelectAddress(validCandidates, dev.AppliedAddress); ok {
						dev.DesiredAddress = desiredIP.String()
					} else {
						dev.DesiredAddress = ""
					}
				}
				_ = s.DeviceStateService.Save(dev)
			}
		}
	}

	if changed && s.DDNSService != nil {
		go func() {
			_ = s.DDNSService.ReconcileNetwork(context.Background(), req.NetworkID)
		}()
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"accepted_revision": st.Revision,
		"changed":           changed,
	})
}

func (s *Server) getNetworkPrefixes(w http.ResponseWriter, r *http.Request) {
	if s.PrefixStateService == nil {
		http.Error(w, "prefix state service not configured", http.StatusNotImplemented)
		return
	}
	netID := r.PathValue("id")
	st, err := s.PrefixStateService.GetByNetwork(netID)
	if err != nil {
		if errors.Is(err, prefixstate.ErrNetworkNotFound) {
			http.Error(w, "network prefix state not found", http.StatusNotFound)
			return
		}
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	actor := auth.GetActorFromContext(r.Context())
	if actor != nil && actor.Role != auth.RoleOwner && s.Registry != nil {
		if st.RouterDeviceID != "" {
			if visible, exists := s.Registry.IsDeviceVisible(actor.UserID, st.RouterDeviceID); !visible || !exists {
				http.Error(w, "network prefix state not found", http.StatusNotFound)
				return
			}
		}
	}

	writeJSON(w, http.StatusOK, st)
}

// GitHub Credential Sync Handlers & Push helpers

func (s *Server) githubDeviceCode(w http.ResponseWriter, r *http.Request) {
	if s.GitHubSyncService == nil {
		http.Error(w, "github sync service not configured", http.StatusNotImplemented)
		return
	}

	codeResp, err := s.GitHubSyncService.StartDeviceFlow(r.Context())
	if err != nil {
		http.Error(w, "failed to start device flow: "+err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, codeResp)
}

func (s *Server) githubStatus(w http.ResponseWriter, r *http.Request) {
	if s.GitHubSyncService == nil {
		writeJSON(w, http.StatusOK, githubsync.StatusResponse{Connected: false})
		return
	}

	all := s.Registry.List()
	totalEnabled := 0
	syncedDevices := 0
	for _, d := range all {
		if d.GitHubSyncEnabled {
			totalEnabled++
			if d.GitHubStatus == "synced" {
				syncedDevices++
			}
		}
	}

	st := s.GitHubSyncService.GetStatus(totalEnabled, syncedDevices)
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) githubDisconnect(w http.ResponseWriter, r *http.Request) {
	if s.GitHubSyncService == nil {
		http.Error(w, "github sync service not configured", http.StatusNotImplemented)
		return
	}

	deletedCount, err := s.GitHubSyncService.Disconnect(r.Context())
	if err != nil {
		http.Error(w, "disconnect error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Update device states in registry
	for _, d := range s.Registry.List() {
		if d.GitHubSyncEnabled || d.GitHubStatus != "" {
			_ = s.Registry.UpdateGitHubStatus(d.ID, "revoked", 0, "")
		}
	}

	// Broadcast revocation to all connected agents
	s.broadcastGitHubRevoke("account_disconnected")

	writeJSON(w, http.StatusOK, map[string]any{
		"success":      true,
		"deleted_keys": deletedCount,
	})
}

func (s *Server) deviceRegisterGitHubSSHKey(w http.ResponseWriter, r *http.Request) {
	if s.GitHubSyncService == nil {
		http.Error(w, "github sync service not configured", http.StatusNotImplemented)
		return
	}
	devID := r.PathValue("id")
	d, err := s.Registry.Get(devID)
	if err != nil {
		statusError(w, err)
		return
	}

	if !d.GitHubSyncEnabled {
		http.Error(w, "github sync is not enabled for this device", http.StatusForbidden)
		return
	}

	defer r.Body.Close()
	var req githubsync.RegisterSSHKeyRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		http.Error(w, "invalid JSON request: "+err.Error(), http.StatusBadRequest)
		return
	}

	if strings.TrimSpace(req.PublicKey) == "" {
		http.Error(w, "missing public_key", http.StatusBadRequest)
		return
	}

	keyID, err := s.GitHubSyncService.RegisterDeviceKey(r.Context(), devID, d.Hostname, req.PublicKey, req.Fingerprint)
	if err != nil {
		http.Error(w, "failed to register github key: "+err.Error(), http.StatusInternalServerError)
		return
	}

	_ = s.Registry.UpdateGitHubStatus(devID, "key_registered", keyID, req.Fingerprint)

	writeJSON(w, http.StatusOK, map[string]any{
		"status":        "ok",
		"github_key_id": keyID,
	})
}

func (s *Server) pushGitHubSync(deviceID string) error {
	if s.GitHubSyncService == nil || !s.GitHubSyncService.IsConnected() || s.Broker == nil {
		return nil
	}
	payload, err := s.GitHubSyncService.ResolveSyncPayload()
	if err != nil {
		return err
	}
	if s.Commands != nil {
		_, err = s.dispatchSystemCommand(command.KindGitHubSync, deviceID, payload, map[string]any{"version": payload.Version, "hash": payload.Hash}, fmt.Sprintf("github-sync-%d-%s", payload.Version, payload.Hash), s.commandTimeout(command.KindGitHubSync, 2*time.Minute))
		return err
	}
	dataBytes, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	s.Broker.Publish(deviceID, broker.Event{Type: "github_credentials_sync", Data: string(dataBytes)})
	return nil
}

func (s *Server) pushGitHubRevoke(deviceID, reason string) {
	if s.Broker == nil {
		return
	}
	payload := githubsync.RevokePayload{
		Timestamp: time.Now().Unix(),
		Reason:    reason,
	}
	if s.Commands != nil {
		_, _ = s.dispatchSystemCommand(command.KindGitHubRevoke, deviceID, payload, map[string]any{"reason": reason}, "github-revoke-"+reason, s.commandTimeout(command.KindGitHubRevoke, 2*time.Minute))
		return
	}
	dataBytes, _ := json.Marshal(payload)
	s.Broker.Publish(deviceID, broker.Event{Type: "github_credentials_revoke", Data: string(dataBytes)})
}

func (s *Server) dispatchSystemCommand(kind command.Kind, deviceID string, wirePayload, safeRequest any, idempotencyKey string, policy command.TimeoutPolicy) (command.Command, error) {
	safe, err := json.Marshal(safeRequest)
	if err != nil {
		return command.Command{}, err
	}
	c, created, err := s.Commands.Create(command.CreateRequest{Kind: kind, DeviceID: deviceID, RequestedBy: command.Actor{Type: "system", ID: "homeagent-server"}, IdempotencyKey: idempotencyKey, Request: safe, TimeoutPolicy: policy, Protocol: 1})
	if err != nil {
		return command.Command{}, err
	}
	if !created {
		return c, nil
	}
	c, err = s.Commands.StartDispatch(c.ID)
	if err != nil {
		return command.Command{}, err
	}
	raw, err := json.Marshal(wirePayload)
	if err != nil {
		return command.Command{}, err
	}
	var envelope map[string]any
	if err = json.Unmarshal(raw, &envelope); err != nil {
		return command.Command{}, err
	}
	envelope["command_id"] = c.ID
	envelope["protocol"] = 1
	ackMode := "legacy"
	if d, e := s.Registry.Get(deviceID); e == nil && supportsControlProtocol(d, 1) {
		ackMode = "two_phase"
	}
	envelope["ack_mode"] = ackMode
	raw, err = json.Marshal(envelope)
	if err != nil {
		return command.Command{}, err
	}
	listeners := s.Broker.Publish(deviceID, broker.Event{Type: string(kind), Data: string(raw), ID: string(c.ID)})
	return s.finishDispatch(c, listeners)
}

func (s *Server) broadcastGitHubSync() {
	if s.GitHubSyncService == nil || !s.GitHubSyncService.IsConnected() || s.Broker == nil {
		return
	}
	for _, d := range s.Registry.List() {
		if d.GitHubSyncEnabled && s.Broker.IsConnected(d.ID) {
			_ = s.pushGitHubSync(d.ID)
		}
	}
}

func (s *Server) broadcastGitHubRevoke(reason string) {
	if s.Broker == nil {
		return
	}
	for _, d := range s.Registry.List() {
		if s.Broker.IsConnected(d.ID) {
			s.pushGitHubRevoke(d.ID, reason)
		}
	}
}

func (s *Server) githubAvatar(w http.ResponseWriter, r *http.Request) {
	if s.GitHubSyncService == nil {
		http.NotFound(w, r)
		return
	}

	data, ct, err := s.GitHubSyncService.GetAvatar(r.Context())
	if err != nil {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(data)
}

func resolveServerHost(r *http.Request) string {
	if r == nil {
		lanIP := detectPrimaryLANIP()
		if lanIP != "" {
			return lanIP + ":8080"
		}
		return "127.0.0.1:8080"
	}
	host := r.Host
	if xfh := r.Header.Get("X-Forwarded-Host"); xfh != "" {
		host = strings.TrimSpace(strings.Split(xfh, ",")[0])
	}
	if host == "" {
		host = "127.0.0.1:8080"
	}

	hostname, port, err := net.SplitHostPort(host)
	if err != nil {
		hostname = host
		port = ""
	}

	// 如果请求 Host 为回环地址（127.0.0.1 / localhost / ::1），尝试替换为本机首选局域网 IP
	if hostname == "127.0.0.1" || hostname == "localhost" || hostname == "::1" || hostname == "" {
		lanIP := detectPrimaryLANIP()
		if lanIP != "" {
			if port != "" {
				return net.JoinHostPort(lanIP, port)
			}
			return lanIP
		}
	}

	return host
}

func detectPrimaryLANIP() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		name := strings.ToLower(iface.Name)
		// 忽略常见虚拟网卡
		if strings.HasPrefix(name, "utun") || strings.HasPrefix(name, "bridge") ||
			strings.HasPrefix(name, "docker") || strings.HasPrefix(name, "veth") ||
			strings.HasPrefix(name, "tailscale") || strings.HasPrefix(name, "virbr") ||
			strings.HasPrefix(name, "cni") {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipNet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipNet.IP.To4()
			if ip != nil && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() {
				return ip.String()
			}
		}
	}
	return ""
}

func (s *Server) handleHealthSummary(w http.ResponseWriter, r *http.Request) {
	if s.Health == nil {
		http.Error(w, "health service unavailable", http.StatusServiceUnavailable)
		return
	}
	summary, err := s.Health.GetSummary(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

func (s *Server) handleDeviceHealth(w http.ResponseWriter, r *http.Request) {
	if s.Health == nil {
		http.Error(w, "health service unavailable", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")
	snap, err := s.Health.GetSnapshot(r.Context(), id)
	if err != nil {
		if evaluated, evalErr := s.Health.EvaluateDevice(r.Context(), id); evalErr == nil {
			writeJSON(w, http.StatusOK, evaluated)
			return
		}
		statusError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

func (s *Server) handleDeviceHealthEvents(w http.ResponseWriter, r *http.Request) {
	if s.Health == nil {
		http.Error(w, "health service unavailable", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")
	cursor := r.URL.Query().Get("cursor")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	events, nextCursor, err := s.Health.ListEvents(r.Context(), id, cursor, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"events":      events,
		"next_cursor": nextCursor,
	})
}

func (s *Server) handleListAlerts(w http.ResponseWriter, r *http.Request) {
	if s.Alerting == nil {
		http.Error(w, "alerting service unavailable", http.StatusServiceUnavailable)
		return
	}

	actor := auth.GetActorFromContext(r.Context())
	isOwner := actor == nil || actor.Role == auth.RoleOwner

	q := r.URL.Query()
	reqDevID := q.Get("device_id")
	if !isOwner && s.Registry != nil && reqDevID != "" {
		if visible, exists := s.Registry.IsDeviceVisible(actor.UserID, reqDevID); !visible || !exists {
			writeJSON(w, http.StatusOK, map[string]any{"alerts": []any{}, "next_cursor": ""})
			return
		}
	}

	limit, _ := strconv.Atoi(q.Get("limit"))
	filter := alerting.AlertFilter{
		DeviceID: reqDevID,
		State:    q.Get("state"),
		Severity: q.Get("severity"),
		Cursor:   q.Get("cursor"),
		Limit:    limit,
	}
	alerts, nextCursor, err := s.Alerting.ListAlerts(r.Context(), filter)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if !isOwner && s.Registry != nil {
		visibleDevs := s.Registry.FilterDevicesForUser(actor.UserID, false)
		visibleMap := make(map[string]bool)
		for _, d := range visibleDevs {
			visibleMap[d.ID] = true
		}
		var filtered []alerting.Alert
		for _, a := range alerts {
			if visibleMap[a.DeviceID] {
				filtered = append(filtered, a)
			}
		}
		alerts = filtered
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"alerts":      alerts,
		"next_cursor": nextCursor,
	})
}

func (s *Server) handleGetAlert(w http.ResponseWriter, r *http.Request) {
	if s.Alerting == nil {
		http.Error(w, "alerting service unavailable", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")
	alert, err := s.Alerting.GetAlert(r.Context(), id)
	if err != nil {
		statusError(w, err)
		return
	}

	actor := auth.GetActorFromContext(r.Context())
	if actor != nil && actor.Role != auth.RoleOwner && s.Registry != nil {
		if visible, exists := s.Registry.IsDeviceVisible(actor.UserID, alert.DeviceID); !visible || !exists {
			http.Error(w, "alert not found", http.StatusNotFound)
			return
		}
	}

	writeJSON(w, http.StatusOK, alert)
}

func (s *Server) handleCreateSilence(w http.ResponseWriter, r *http.Request) {
	if s.Alerting == nil {
		http.Error(w, "alerting service unavailable", http.StatusServiceUnavailable)
		return
	}
	var req struct {
		DeviceID   string    `json:"device_id"`
		ReasonCode string    `json:"reason_code"`
		StartsAt   time.Time `json:"starts_at"`
		EndsAt     time.Time `json:"ends_at"`
		Comment    string    `json:"comment"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON request: "+err.Error(), http.StatusBadRequest)
		return
	}

	actor := auth.GetActorFromContext(r.Context())
	actorName := "admin"
	if actor != nil {
		actorName = actor.Username
		if actor.Role != auth.RoleOwner && s.Registry != nil && req.DeviceID != "" {
			if visible, exists := s.Registry.IsDeviceVisible(actor.UserID, req.DeviceID); !visible || !exists {
				http.Error(w, "device not found", http.StatusNotFound)
				return
			}
		}
	}

	sil := alerting.Silence{
		DeviceID:   req.DeviceID,
		ReasonCode: req.ReasonCode,
		StartsAt:   req.StartsAt,
		EndsAt:     req.EndsAt,
		CreatedBy:  actorName,
		Comment:    req.Comment,
	}
	created, err := s.Alerting.CreateSilence(r.Context(), sil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) handleDeleteSilence(w http.ResponseWriter, r *http.Request) {
	if s.Alerting == nil {
		http.Error(w, "alerting service unavailable", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")
	if err := s.Alerting.DeleteSilence(r.Context(), id); err != nil {
		statusError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) handleListSilences(w http.ResponseWriter, r *http.Request) {
	if s.Alerting == nil {
		http.Error(w, "alerting service unavailable", http.StatusServiceUnavailable)
		return
	}
	silences, err := s.Alerting.ListSilences(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"silences": silences})
}

func (s *Server) handleListAlertDeliveries(w http.ResponseWriter, r *http.Request) {
	if s.Alerting == nil {
		http.Error(w, "alerting service unavailable", http.StatusServiceUnavailable)
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	filter := alerting.DeliveryFilter{
		AlertID:   q.Get("alert_id"),
		ChannelID: q.Get("channel_id"),
		Cursor:    q.Get("cursor"),
		Limit:     limit,
	}
	attempts, nextCursor, err := s.Alerting.ListDeliveryAttempts(r.Context(), filter)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"deliveries":  attempts,
		"next_cursor": nextCursor,
	})
}

func (s *Server) handleTestAlertChannel(w http.ResponseWriter, r *http.Request) {
	if s.Alerting == nil {
		http.Error(w, "alerting service unavailable", http.StatusServiceUnavailable)
		return
	}

	actor := auth.GetActorFromContext(r.Context())
	if actor != nil && actor.Role != auth.RoleOwner {
		http.Error(w, `{"error":"forbidden","message":"Only instance owners can test global alert channels"}`, http.StatusForbidden)
		return
	}

	id := r.PathValue("id")
	res, err := s.Alerting.TestChannel(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status_code":   res.StatusCode,
		"retryable":     res.Retryable,
		"error_code":    res.ErrorCode,
		"error_message": res.ErrorMessage,
	})
}

func (s *Server) systemVersionCheck(w http.ResponseWriter, r *http.Request) {
	ghClient := s.GitHubReleaseClient
	if ghClient == nil {
		ghClient = githubrelease.NewClient(githubrelease.Config{
			Repo:         s.GitHubRepo,
			MirrorPrefix: s.GitHubMirrorPrefix,
		})
	}

	forceRefresh := r.URL.Query().Get("refresh") == "true" || r.URL.Query().Get("force") == "true"
	rel, err := ghClient.GetLatestRelease(r.Context(), forceRefresh)
	currentVer := version.Get()

	if err != nil {
		if s.Log != nil {
			s.Log.Warn("system_version_check_failed", "error", err)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"current_version": currentVer,
			"latest_version":  currentVer,
			"has_update":      false,
			"error":           err.Error(),
		})
		return
	}

	hasUpdate := githubrelease.CompareVersions(currentVer, rel.TagName) < 0
	writeJSON(w, http.StatusOK, map[string]any{
		"current_version": currentVer,
		"latest_version":  rel.TagName,
		"has_update":      hasUpdate,
		"release_url":     rel.HTMLURL,
		"release_notes":   rel.Body,
		"published_at":    rel.PublishedAt.Format(time.RFC3339),
	})
}

type systemUpgradeReq struct {
	TargetVersion string `json:"target_version,omitempty"`
	Force         bool   `json:"force,omitempty"`
}

func (s *Server) systemUpgrade(w http.ResponseWriter, r *http.Request) {
	var req systemUpgradeReq
	if r.Body != nil && r.ContentLength > 0 {
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		_ = dec.Decode(&req)
	}

	ghClient := s.GitHubReleaseClient
	if ghClient == nil {
		ghClient = githubrelease.NewClient(githubrelease.Config{
			Repo:         s.GitHubRepo,
			MirrorPrefix: s.GitHubMirrorPrefix,
		})
	}

	res, err := serverupgrade.PerformServerSelfUpgrade(r.Context(), serverupgrade.Options{
		TargetVersion: req.TargetVersion,
		Force:         req.Force,
		Client:        ghClient,
		RestartCallback: func() error {
			if s.Log != nil {
				s.Log.Info("server_self_upgrade_exiting_for_restart")
			}
			os.Exit(0)
			return nil
		},
	})
	if err != nil {
		if s.Log != nil {
			s.Log.Error("server_self_upgrade_failed", "error", err)
		}
		http.Error(w, fmt.Sprintf("server self-upgrade failed: %v", err), http.StatusInternalServerError)
		return
	}

	if !res.Updated {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":           "already_up_to_date",
			"message":          fmt.Sprintf("Server is already up to date (%s)", res.TargetVersion),
			"previous_version": res.PreviousVersion,
			"target_version":   res.TargetVersion,
		})
		return
	}

	if s.Log != nil {
		s.Log.Info("server_self_upgrade_succeeded_triggering_restart", "previous", res.PreviousVersion, "target", res.TargetVersion)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":           "upgrading",
		"message":          fmt.Sprintf("Server upgraded from %s to %s, restarting...", res.PreviousVersion, res.TargetVersion),
		"previous_version": res.PreviousVersion,
		"target_version":   res.TargetVersion,
	})
}
