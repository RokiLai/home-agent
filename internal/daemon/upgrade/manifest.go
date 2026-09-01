package upgrade

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ArtifactSpec 描述主应用程序包归档信息。
type ArtifactSpec struct {
	Format              string `json:"format"`
	URL                 string `json:"url"`
	SHA256              string `json:"sha256"`
	SizeBytes           uint64 `json:"size_bytes"`
	RunningBundleDigest string `json:"running_bundle_digest"`
}

// RecoverySpec 描述独立恢复器二进制信息。
type RecoverySpec struct {
	Format                string `json:"format"`
	URL                   string `json:"url"`
	SHA256                string `json:"sha256"`
	SizeBytes             uint64 `json:"size_bytes"`
	DesignatedRequirement string `json:"designated_requirement"`
}

// IdentitySpec 描述平台及 Apple 签名身份特征。
type IdentitySpec struct {
	Component             string `json:"component"`
	OS                    string `json:"os"`
	Arch                  string `json:"arch"`
	BundleID              string `json:"bundle_id"`
	TeamID                string `json:"team_id"`
	DesignatedRequirement string `json:"designated_requirement"`
}

// Signature 包含单枚 Ed25519 签名及公钥标识。
type Signature struct {
	KeyID     string `json:"key_id"`
	Signature string `json:"signature"`
}

// Manifest 定义协议 v2 升级清单严格结构。
type Manifest struct {
	Protocol             int          `json:"protocol"`
	TargetVersion        string       `json:"target_version"`
	MinimumSourceVersion string       `json:"minimum_source_version"`
	ReleaseSequence      uint64       `json:"release_sequence"`
	IssuedAt             int64        `json:"issued_at"`
	ExpiresAt            int64        `json:"expires_at"`
	Artifact             ArtifactSpec `json:"artifact"`
	Recovery             RecoverySpec `json:"recovery"`
	Identity             IdentitySpec `json:"identity"`
	Force                bool         `json:"force"`
	Signatures           []Signature  `json:"signatures"`
}

// KeySet 表示一组用于门限验签的 Ed25519 内置公钥集合。
type KeySet struct {
	SetID     string
	Threshold int
	Keys      map[string]ed25519.PublicKey
}

// EncodeLengthPrefixed 将清单各签名属性按严格顺序转换为确定性长度前缀编码字节。
func (m *Manifest) EncodeLengthPrefixed() []byte {
	buf := new(bytes.Buffer)

	writeUint64 := func(v uint64) {
		b := make([]byte, 8)
		binary.BigEndian.PutUint64(b, v)
		buf.Write(b)
	}

	writeString := func(s string) {
		b := make([]byte, 4)
		binary.BigEndian.PutUint32(b, uint32(len(s)))
		buf.Write(b)
		buf.WriteString(s)
	}

	writeBool := func(v bool) {
		if v {
			buf.WriteByte(0x01)
		} else {
			buf.WriteByte(0x00)
		}
	}

	// 字段顺序必须与设计方案第 5.1 节严格一致
	writeUint64(uint64(m.Protocol))
	writeString(m.TargetVersion)
	writeString(m.MinimumSourceVersion)
	writeUint64(m.ReleaseSequence)
	writeUint64(uint64(m.IssuedAt))
	writeUint64(uint64(m.ExpiresAt))

	// Artifact
	writeString(m.Artifact.Format)
	writeString(m.Artifact.URL)
	writeString(m.Artifact.SHA256)
	writeUint64(m.Artifact.SizeBytes)
	writeString(m.Artifact.RunningBundleDigest)

	// Recovery
	writeString(m.Recovery.Format)
	writeString(m.Recovery.URL)
	writeString(m.Recovery.SHA256)
	writeUint64(m.Recovery.SizeBytes)
	writeString(m.Recovery.DesignatedRequirement)

	// Identity
	writeString(m.Identity.Component)
	writeString(m.Identity.OS)
	writeString(m.Identity.Arch)
	writeString(m.Identity.BundleID)
	writeString(m.Identity.TeamID)
	writeString(m.Identity.DesignatedRequirement)

	// Force
	writeBool(m.Force)

	return buf.Bytes()
}

// ComputeDigest 计算升级清单的规范 SHA256 摘要（小写十六进制 64 字符）。
func (m *Manifest) ComputeDigest() string {
	encoded := m.EncodeLengthPrefixed()
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// ParseManifestStrict 从 JSON 字节流中以严格 Schema 解析升级清单（拒绝未知字段与畸变）。
func ParseManifestStrict(data []byte) (*Manifest, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()

	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("decode manifest: %w", err)
	}
	if dec.More() {
		return nil, errors.New("trailing token in manifest JSON")
	}

	if m.Protocol != 2 {
		return nil, fmt.Errorf("unsupported protocol version %d, expected 2", m.Protocol)
	}
	if m.Artifact.Format != "macos-app-archive-v2" {
		return nil, fmt.Errorf("invalid artifact format %q", m.Artifact.Format)
	}
	if m.Recovery.Format != "macos-recovery-binary-v1" {
		return nil, fmt.Errorf("invalid recovery format %q", m.Recovery.Format)
	}
	if len(m.Artifact.SHA256) != 64 || len(m.Artifact.RunningBundleDigest) != 64 {
		return nil, errors.New("invalid artifact hash length")
	}
	if len(m.Recovery.SHA256) != 64 {
		return nil, errors.New("invalid recovery hash length")
	}

	return &m, nil
}

// VerifySignatures 验证清单签名是否满足指定公钥集合的门限要求（如 2-of-3）。
func (m *Manifest) VerifySignatures(activeSets []KeySet) error {
	if len(m.Signatures) == 0 {
		return errors.New("no signatures present in manifest")
	}

	encoded := m.EncodeLengthPrefixed()

	for _, set := range activeSets {
		validCount := 0
		seenKeys := make(map[string]bool)

		for _, sig := range m.Signatures {
			if seenKeys[sig.KeyID] {
				return fmt.Errorf("duplicate key_id %q in signatures", sig.KeyID)
			}
			seenKeys[sig.KeyID] = true

			pubKey, ok := set.Keys[sig.KeyID]
			if !ok {
				continue
			}

			sigBytes, err := base64.StdEncoding.DecodeString(sig.Signature)
			if err != nil {
				return fmt.Errorf("invalid base64 signature for key %s: %w", sig.KeyID, err)
			}
			if len(sigBytes) != ed25519.SignatureSize {
				return fmt.Errorf("invalid signature size %d for key %s", len(sigBytes), sig.KeyID)
			}

			if ed25519.Verify(pubKey, encoded, sigBytes) {
				validCount++
			}
		}

		if validCount < set.Threshold {
			return fmt.Errorf("key set %s threshold not met: got %d valid signatures, want %d", set.SetID, validCount, set.Threshold)
		}
	}

	return nil
}

// ValidateTimeWindow 校验清单有效期及签发时间窗口。
func (m *Manifest) ValidateTimeWindow(now time.Time) error {
	nowSec := now.Unix()
	// 允许签发时间早于当前时间最多 5 分钟（时钟微偏容差）
	if m.IssuedAt > nowSec+300 {
		return fmt.Errorf("manifest issued in the future (issued_at=%d, now=%d)", m.IssuedAt, nowSec)
	}
	if m.ExpiresAt < nowSec {
		return fmt.Errorf("manifest expired (expires_at=%d, now=%d)", m.ExpiresAt, nowSec)
	}
	return nil
}

// ParseSemVer 将标准 vMAJOR.MINOR.PATCH 解析为三个无符号整数。
func ParseSemVer(v string) (uint64, uint64, uint64, error) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return 0, 0, 0, fmt.Errorf("invalid semver %q, expected MAJOR.MINOR.PATCH", v)
	}

	major, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("invalid major %q: %w", parts[0], err)
	}
	minor, err := strconv.ParseUint(parts[1], 10, 64)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("invalid minor %q: %w", parts[1], err)
	}
	patch, err := strconv.ParseUint(parts[2], 10, 64)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("invalid patch %q: %w", parts[2], err)
	}

	return major, minor, patch, nil
}

// CompareSemVer 比较两个版本号，v1 > v2 返回 1，v1 < v2 返回 -1，相等返回 0。
func CompareSemVer(v1, v2 string) (int, error) {
	maj1, min1, pat1, err := ParseSemVer(v1)
	if err != nil {
		return 0, err
	}
	maj2, min2, pat2, err := ParseSemVer(v2)
	if err != nil {
		return 0, err
	}

	if maj1 != maj2 {
		if maj1 > maj2 {
			return 1, nil
		}
		return -1, nil
	}
	if min1 != min2 {
		if min1 > min2 {
			return 1, nil
		}
		return -1, nil
	}
	if pat1 != pat2 {
		if pat1 > pat2 {
			return 1, nil
		}
		return -1, nil
	}
	return 0, nil
}
