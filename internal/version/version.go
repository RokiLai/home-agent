// Package version 提供 HomeAgent 组件的版本元数据及构建时版本信息提取。
package version

import "strings"

const defaultServerVersion = "v0.6.34"
const defaultAgentVersion = "v0.6.17"

// ServerVersion 与 AgentVersion 是两个组件可独立注入的当前版本号。
// -ldflags "-X homeagent/internal/version.ServerVersion=vX.Y.Z -X homeagent/internal/version.AgentVersion=vX.Y.Z"
var (
	ServerVersion = defaultServerVersion
	AgentVersion  = defaultAgentVersion
)

func normalized(value, fallback string) string {
	v := strings.TrimSpace(value)
	if v == "" {
		return fallback
	}
	return v
}

// GetServer 返回服务端版本。
func GetServer() string { return normalized(ServerVersion, defaultServerVersion) }

// GetAgent 返回客户端版本。
func GetAgent() string { return normalized(AgentVersion, defaultAgentVersion) }

// Get 是迁移期兼容入口；新代码必须使用组件明确入口。
// Deprecated: use GetServer or GetAgent.
func Get() string { return GetServer() }
