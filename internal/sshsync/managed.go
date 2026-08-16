package sshsync

import (
	"bufio"
	"bytes"
	"fmt"
	"strings"
)

const Begin = "# BEGIN HOMEAGENT MANAGED"
const End = "# END HOMEAGENT MANAGED"

type Key struct {
	DeviceID  string `json:"device_id"`
	PublicKey string `json:"public_key"`
}
type KeySet struct {
	Keys []Key `json:"keys"`
}

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
