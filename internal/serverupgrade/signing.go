package serverupgrade

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"runtime"
	"strings"
)

const (
	// EnvServerCodesignIdentity 是用于 macOS 服务端自升级签名的证书指纹环境变量。
	EnvServerCodesignIdentity = "HOMEAGENT_SERVER_CODESIGN_IDENTITY"

	// DefaultSignerIdentifier 是服务端固定的代码签名标识。
	DefaultSignerIdentifier = "online.rokilai.homeagent-server"
)

// SignerConfig 包含代码签名的不可变快照配置。
type SignerConfig struct {
	Enabled     bool
	Fingerprint string
	Identifier  string
}

// Signer 定义代码签名的操作契约。
type Signer interface {
	Preflight(ctx context.Context) error
	Sign(ctx context.Context, targetPath string) error
	VerifyCandidate(ctx context.Context, targetPath string) error
	ReadDesignatedRequirement(ctx context.Context, targetPath string) (requirement string, isAdhoc bool, err error)
	VerifyRequirement(ctx context.Context, targetPath, requirement string) error
}

// LoadSignerConfig 从环境变量读取并校验代码签名配置。
func LoadSignerConfig() (SignerConfig, error) {
	raw := os.Getenv(EnvServerCodesignIdentity)
	val := strings.TrimSpace(raw)
	if val == "" {
		return SignerConfig{Enabled: false}, nil
	}

	if runtime.GOOS != "darwin" {
		return SignerConfig{}, fmt.Errorf("code signing is only supported on macOS (current: %s)", runtime.GOOS)
	}

	// 必须为 40 字符十六进制指纹
	if len(val) != 40 {
		return SignerConfig{}, fmt.Errorf("invalid %s: expected 40-character hex SHA-1 fingerprint, got length %d", EnvServerCodesignIdentity, len(val))
	}
	if _, err := hex.DecodeString(val); err != nil {
		return SignerConfig{}, fmt.Errorf("invalid %s: expected valid hex characters: %w", EnvServerCodesignIdentity, err)
	}

	return SignerConfig{
		Enabled:     true,
		Fingerprint: strings.ToUpper(val),
		Identifier:  DefaultSignerIdentifier,
	}, nil
}
