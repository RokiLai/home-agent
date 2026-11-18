// Package routermonitor translates Router Guard observations into console data.
package routermonitor

import (
	"bufio"
	"errors"
	"io"
	"strconv"
	"strings"
)

type Check struct {
	Component string `json:"component"`
	Status    string `json:"status"`
	Detail    string `json:"detail"`
}

type Snapshot struct {
	Mode          string  `json:"mode"`
	CurrentUptime int64   `json:"current_uptime"`
	CheckedUptime int64   `json:"checked_uptime"`
	Checks        []Check `json:"checks"`
}

var errSnapshot = errors.New("invalid Router Guard snapshot")

func ParseSnapshot(r io.Reader) (Snapshot, error) {
	s := Snapshot{Checks: []Check{}}
	scan := bufio.NewScanner(r)
	if !scan.Scan() {
		return Snapshot{}, errSnapshot
	}
	header := strings.Fields(scan.Text())
	if len(header) != 2 {
		return Snapshot{}, errSnapshot
	}
	pause, err := uptimeField(header[0], "pause_until_uptime=")
	if err != nil {
		return Snapshot{}, err
	}
	s.CurrentUptime, err = uptimeField(header[1], "current_uptime=")
	if err != nil {
		return Snapshot{}, err
	}
	seen := map[string]bool{}
	checked := false
	for scan.Scan() {
		line := scan.Text()
		if line == "" || line == "No completed health cycle yet." {
			continue
		}
		fields := strings.SplitN(line, "\t", 3)
		if fields[0] == "checked_uptime" {
			if checked || len(fields) != 2 {
				return Snapshot{}, errSnapshot
			}
			s.CheckedUptime, err = uptimeField(fields[1], "")
			if err != nil || s.CheckedUptime > s.CurrentUptime {
				return Snapshot{}, errSnapshot
			}
			checked = true
			continue
		}
		if len(fields) != 3 || fields[0] == "" || seen[fields[0]] || len(s.Checks) >= 128 {
			return Snapshot{}, errSnapshot
		}
		seen[fields[0]] = true
		status := fields[1]
		switch status {
		case "healthy", "failed", "skipped":
		default:
			status = "unknown"
		}
		s.Checks = append(s.Checks, Check{fields[0], status, fields[2]})
	}
	if err := scan.Err(); err != nil {
		return Snapshot{}, err
	}
	if !checked && len(s.Checks) > 0 {
		return Snapshot{}, errSnapshot
	}
	switch {
	case pause > s.CurrentUptime:
		s.Mode = "paused"
	case !checked:
		s.Mode = "waiting"
	case s.CurrentUptime-s.CheckedUptime > 180:
		s.Mode = "stale"
	default:
		s.Mode = "monitoring"
	}
	return s, nil
}

func uptimeField(value, prefix string) (int64, error) {
	raw, ok := strings.CutPrefix(value, prefix)
	n, err := strconv.ParseInt(raw, 10, 64)
	if !ok || err != nil || n < 0 {
		return 0, errSnapshot
	}
	return n, nil
}
