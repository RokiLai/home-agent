package acl

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"homeagent/internal/device"
)

type Policy struct {
	DefaultAllow bool
	Devices      map[string][]string
}

func Load(path string) (Policy, error) {
	p := Policy{DefaultAllow: true, Devices: map[string][]string{}}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	defer f.Close()
	var target string
	inAllow := false
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimRight(s.Text(), " \t\r")
		trim := strings.TrimSpace(strings.SplitN(line, "#", 2)[0])
		if trim == "" {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		switch {
		case strings.HasPrefix(trim, "default_policy:"):
			v := strings.TrimSpace(strings.TrimPrefix(trim, "default_policy:"))
			if v != "allow_all" && v != "deny" {
				return p, fmt.Errorf("invalid default_policy %q", v)
			}
			p.DefaultAllow = v == "allow_all"
		case indent == 2 && strings.HasSuffix(trim, ":") && trim != "devices:":
			target = strings.TrimSuffix(trim, ":")
			p.Devices[target] = nil
			inAllow = false
		case indent == 4 && trim == "allow:":
			inAllow = true
		case indent >= 6 && inAllow && strings.HasPrefix(trim, "-"):
			p.Devices[target] = append(p.Devices[target], strings.TrimSpace(strings.TrimPrefix(trim, "-")))
		}
	}
	return p, s.Err()
}

func (p Policy) Resolve(target string, all []device.Device) []device.Device {
	allowed, explicit := p.Devices[target]
	set := map[string]bool{}
	if explicit {
		for _, id := range allowed {
			set[id] = true
		}
	}
	var out []device.Device
	for _, d := range all {
		if d.ID == target {
			continue
		}
		if explicit && set[d.ID] || !explicit && p.DefaultAllow {
			out = append(out, d)
		}
	}
	return out
}
