// Package acl 提供用于设备间 SSH 互访授权的访问控制策略解析与规则匹配计算。
package acl

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"homeagent/internal/device"
)

// Policy 表示已解析的访问控制策略规则，用于决定哪些源设备被允许访问目标设备。
type Policy struct {
	// DefaultAllow 指定在没有显式配置规则时，是否默认放行访问。
	DefaultAllow bool
	// Devices 维护目标设备 ID 到被允许访问它的源设备 ID 列表的映射关系。
	Devices map[string][]string
}

// Load 从指定路径读取并解析 ACL 策略 YAML 配置文件。
// 若文件不存在，则默认返回全放行（allow-all）策略且不报错。
// 文件格式支持如下语法：
//
//	default_policy: allow_all | deny
//	devices:
//	  <target_id>:
//	    allow:
//	      - <source_id_1>
//	      - <source_id_2>
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

// Resolve 根据全量设备列表计算目标设备允许接入的设备清单。
// 返回的设备公钥将被部署至目标设备的 authorized_keys 中。
// 规则：目标设备自身始终默认允许自访问；若显式配置了 allow 列表则严格匹配；否则依据 DefaultAllow 决定。
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
		if d.ID == target || explicit && set[d.ID] || !explicit && p.DefaultAllow {
			out = append(out, d)
		}
	}
	return out
}
