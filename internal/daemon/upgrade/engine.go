package upgrade

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
)

// GenerateFenceToken 生成 32 字节高强度随机 Fence Token（Base64URL 无填充格式）。
func GenerateFenceToken() (string, []byte, error) {
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", nil, fmt.Errorf("read random bytes for fence token: %w", err)
	}
	tokenStr := base64.RawURLEncoding.EncodeToString(tokenBytes)
	return tokenStr, tokenBytes, nil
}

// ComputeFenceDigest 计算 Fence Token 的 SHA256 摘要（小写 64 字符十六进制）。
func ComputeFenceDigest(tokenBytes []byte) string {
	sum := sha256.Sum256(tokenBytes)
	return hex.EncodeToString(sum[:])
}

// FactsDigestParams 包含计算 Fenced Facts 确定性摘要的输入参数。
type FactsDigestParams struct {
	DeviceID            string
	CommandID           string
	TransactionID       string
	TargetVersion       string
	UpgradeSecurityMode string
	FenceRevision       uint64
	ReleaseSequence     uint64
	FenceTokenDigest    string // 64-hex
	ManifestDigest      string // 64-hex
	RunningBundleDigest string // 64-hex
}

// ComputeFactsDigest 根据设计方案第 5.3.1 节规范编码并计算 facts_digest。
func ComputeFactsDigest(params FactsDigestParams) (string, error) {
	buf := new(bytes.Buffer)

	writeString := func(s string) {
		b := make([]byte, 4)
		binary.BigEndian.PutUint32(b, uint32(len(s)))
		buf.Write(b)
		buf.WriteString(s)
	}

	writeUint64 := func(v uint64) {
		b := make([]byte, 8)
		binary.BigEndian.PutUint64(b, v)
		buf.Write(b)
	}

	writeHex32 := func(h string) error {
		if len(h) != 64 {
			return fmt.Errorf("invalid hex string length %d, want 64", len(h))
		}
		raw, err := hex.DecodeString(h)
		if err != nil {
			return fmt.Errorf("decode hex %q: %w", h, err)
		}
		buf.Write(raw)
		return nil
	}

	// 1. device_id (4-byte len + UTF-8)
	writeString(params.DeviceID)
	// 2. command_id (4-byte len + UTF-8)
	writeString(params.CommandID)
	// 3. transaction_id (4-byte len + UTF-8)
	writeString(params.TransactionID)
	// 4. target_version (4-byte len + UTF-8)
	writeString(params.TargetVersion)
	// 5. upgrade_security_mode (4-byte len + UTF-8)
	writeString(params.UpgradeSecurityMode)
	// 6. fence_revision (8-byte big-endian uint64)
	writeUint64(params.FenceRevision)
	// 7. release_sequence (8-byte big-endian uint64)
	writeUint64(params.ReleaseSequence)

	// 8. SHA256(fence_token) raw 32 bytes
	if err := writeHex32(params.FenceTokenDigest); err != nil {
		return "", err
	}
	// 9. manifest_digest raw 32 bytes
	if err := writeHex32(params.ManifestDigest); err != nil {
		return "", err
	}
	// 10. running_bundle_digest raw 32 bytes
	if err := writeHex32(params.RunningBundleDigest); err != nil {
		return "", err
	}

	sum := sha256.Sum256(buf.Bytes())
	return hex.EncodeToString(sum[:]), nil
}

// UpgradeConfirmation 定义服务端返回的准备/提交确认对象。
type UpgradeConfirmation struct {
	State         string `json:"state"` // "prepared" | "committed"
	CommandID     string `json:"command_id"`
	FenceRevision uint64 `json:"fence_revision"`
	FenceDigest   string `json:"fence_digest"`
	FactsDigest   string `json:"facts_digest"`
	ServerNonce   string `json:"server_nonce"`
}

// ValidateConfirmation 校验服务端返回的确认对象完整性。
func (c *UpgradeConfirmation) ValidateConfirmation(expectedState, expectedCmdID string, expectedRevision uint64, expectedFenceDigest, expectedFactsDigest string) error {
	if c.State != expectedState {
		return fmt.Errorf("confirmation state mismatch: got %q, want %q", c.State, expectedState)
	}
	if c.CommandID != expectedCmdID {
		return fmt.Errorf("confirmation command_id mismatch: got %q, want %q", c.CommandID, expectedCmdID)
	}
	if c.FenceRevision != expectedRevision {
		return fmt.Errorf("confirmation fence_revision mismatch: got %d, want %d", c.FenceRevision, expectedRevision)
	}
	if c.FenceDigest != expectedFenceDigest {
		return fmt.Errorf("confirmation fence_digest mismatch: got %q, want %q", c.FenceDigest, expectedFenceDigest)
	}
	if c.FactsDigest != expectedFactsDigest {
		return fmt.Errorf("confirmation facts_digest mismatch: got %q, want %q", c.FactsDigest, expectedFactsDigest)
	}
	if c.ServerNonce == "" {
		return errors.New("missing server_nonce in upgrade confirmation")
	}
	return nil
}
