// Package version 提供 HomeAgent 组件的版本元数据及构建时版本信息提取。
package version

import "strings"

const defaultVersion = "v0.6.12"

// Version 是 HomeAgent 组件的当前版本号。
// 可在编译时通过以下方式覆盖：
// -ldflags "-X homeagent/internal/version.Version=vX.Y.Z"
var Version = defaultVersion

// Get 返回当前版本字符串，若未设置或为空则返回默认版本号。
func Get() string {
	v := strings.TrimSpace(Version)
	if v == "" {
		return defaultVersion
	}
	return v
}
