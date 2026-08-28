// Package sshsync 提供 authorized_keys 托管块管理、公钥集合哈希计算及 SSH 公钥同步控制器。
package sshsync

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// Begin 是标记 HomeAgent 托管 authorized_keys 块起始位置的哨兵注释行。
const Begin = "# BEGIN HOMEAGENT MANAGED"

// End 是标记 HomeAgent 托管 authorized_keys 块结束位置的哨兵注释行。
const End = "# END HOMEAGENT MANAGED"

// Key 表示单台设备的 SSH 公钥条目。
type Key struct {
	DeviceID  string `json:"device_id"`
	PublicKey string `json:"public_key"`
}

// KeySet 表示待部署的设备公钥集合。
type KeySet struct {
	Keys []Key `json:"keys"`
}

// KeySyncPayload 表示通过 SSE 推送给客户端 Agent 的 key_sync 事件载荷。
type KeySyncPayload struct {
	Version int64  `json:"version"`
	Hash    string `json:"hash"`
	Keys    []Key  `json:"keys"`
}

// ComputeKeySetHash 对 SSH 公钥切片计算确定性的 SHA256 十六进制摘要，用于版本与内容一致性校验。
func ComputeKeySetHash(keys []Key) string {

	var lines []string
	seen := map[string]bool{}
	for _, k := range keys {
		fields := strings.Fields(k.PublicKey)
		if len(fields) < 2 {
			continue
		}
		identity := fields[0] + " " + fields[1]
		if seen[identity] {
			continue
		}
		seen[identity] = true
		lines = append(lines, identity+" "+sanitizeComment(k.DeviceID))
	}
	sort.Strings(lines)
	h := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(h[:])
}

// UpdateManagedBlock 在已有 authorized_keys 内容中原子替换或追加 HomeAgent 托管公钥块，同时完整保留用户自身原有密钥。
func UpdateManagedBlock(existing []byte, keys []Key) ([]byte, error) {

	seen := map[string]bool{}
	var block []string
	for _, k := range keys {
		fields := strings.Fields(k.PublicKey)
		if len(fields) < 2 {
			return nil, fmt.Errorf("invalid public key for %s", k.DeviceID)
		}
		identity := fields[0] + " " + fields[1]
		if seen[identity] {
			continue
		}
		seen[identity] = true
		block = append(block, identity+" "+sanitizeComment(k.DeviceID))
	}
	lines := scanLines(existing)
	start, end := -1, -1
	for i, line := range lines {
		if line == Begin {
			if start >= 0 {
				return nil, fmt.Errorf("multiple managed blocks")
			}
			start = i
		}
		if line == End {
			if start < 0 || end >= 0 {
				return nil, fmt.Errorf("malformed managed block")
			}
			end = i
		}
	}
	if (start >= 0) != (end >= 0) || end >= 0 && end < start {
		return nil, fmt.Errorf("malformed managed block")
	}
	replacement := append([]string{Begin}, block...)
	replacement = append(replacement, End)
	if start >= 0 {
		lines = append(append(append([]string{}, lines[:start]...), replacement...), lines[end+1:]...)
	} else {
		if len(lines) > 0 && lines[len(lines)-1] != "" {
			lines = append(lines, "")
		}
		lines = append(lines, replacement...)
	}
	return []byte(strings.Join(lines, "\n") + "\n"), nil
}

func scanLines(b []byte) []string {
	b = bytes.TrimSuffix(b, []byte("\n"))
	if len(b) == 0 {
		return nil
	}
	var lines []string
	s := bufio.NewScanner(bytes.NewReader(b))
	for s.Scan() {
		lines = append(lines, strings.TrimSuffix(s.Text(), "\r"))
	}
	return lines
}

func sanitizeComment(s string) string {
	return strings.Map(func(r rune) rune {
		if r == ' ' || r == '\t' || r == '\r' || r == '\n' {
			return '-'
		}
		return r
	}, s)
}
