package domainbinding

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
)

type ConfigLoadState string

const (
	ConfigUnconfigured        ConfigLoadState = "unconfigured"
	ConfigInvalid             ConfigLoadState = "invalid"
	ConfigInsecurePermissions ConfigLoadState = "insecure_permissions"
	ConfigReady               ConfigLoadState = "ready"
)

type Config struct {
	APIToken      string `json:"api_token"`
	ZoneID        string `json:"zone_id"`
	ManagedSuffix string `json:"managed_suffix"`
}

func (config Config) Token() string { return config.APIToken }

type ConfigResult struct {
	Enabled    bool
	State      ConfigLoadState
	Diagnostic string
	Config     *Config
}

func LoadConfig(path string) ConfigResult {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return ConfigResult{State: ConfigUnconfigured, Diagnostic: "Cloudflare DDNS 未配置"}
	}
	if err != nil {
		return ConfigResult{State: ConfigInvalid, Diagnostic: "无法读取 Cloudflare DDNS 配置"}
	}
	if !hasSecurePermissions(info) {
		return ConfigResult{State: ConfigInsecurePermissions, Diagnostic: "Cloudflare DDNS 配置的所有者或权限不安全"}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ConfigResult{State: ConfigInvalid, Diagnostic: "无法读取 Cloudflare DDNS 配置"}
	}
	var config Config
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&config) != nil || strings.TrimSpace(config.APIToken) == "" || strings.TrimSpace(config.ZoneID) == "" || strings.TrimSpace(config.ManagedSuffix) != ManagedSuffix {
		return ConfigResult{State: ConfigInvalid, Diagnostic: "Cloudflare DDNS 配置无效"}
	}
	config.APIToken = strings.TrimSpace(config.APIToken)
	config.ZoneID = strings.TrimSpace(config.ZoneID)
	config.ManagedSuffix = ManagedSuffix
	return ConfigResult{Enabled: true, State: ConfigReady, Diagnostic: "Cloudflare DDNS 配置已就绪", Config: &config}
}
