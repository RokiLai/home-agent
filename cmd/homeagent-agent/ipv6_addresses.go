package main

import (
	"encoding/json"
	"net"
	"strconv"
	"strings"

	"homeagent/internal/device"
)

func parseLinuxStableIPv6(output string) []string {
	var values []string
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 || fields[2] != "inet6" {
			continue
		}
		iface := strings.TrimSuffix(fields[1], ":")
		if isVirtualInterface(iface) || !containsField(fields, "global") || containsAnyField(fields, "temporary", "deprecated", "tentative", "dadfailed") {
			continue
		}
		values = append(values, strings.Split(fields[3], "/")[0])
	}
	return device.FilterAndSortAddresses(values)
}

type windowsIPv6Record struct {
	IPAddress      string          `json:"IPAddress"`
	InterfaceAlias string          `json:"InterfaceAlias"`
	AddressState   json.RawMessage `json:"AddressState"`
	SuffixOrigin   json.RawMessage `json:"SuffixOrigin"`
}

func parseWindowsStableIPv6(output string) []string {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return nil
	}
	var records []windowsIPv6Record
	if strings.HasPrefix(trimmed, "[") {
		if err := json.Unmarshal([]byte(trimmed), &records); err != nil {
			return nil
		}
	} else {
		var record windowsIPv6Record
		if err := json.Unmarshal([]byte(trimmed), &record); err != nil {
			return nil
		}
		records = []windowsIPv6Record{record}
	}

	var values []string
	for _, record := range records {
		if !windowsEnumEquals(record.AddressState, "Preferred", 4) || windowsEnumEquals(record.SuffixOrigin, "Random", 5) || len(record.SuffixOrigin) == 0 || isVirtualInterface(record.InterfaceAlias) {
			continue
		}
		if ip := net.ParseIP(strings.TrimSpace(record.IPAddress)); ip != nil && ip.To4() == nil {
			values = append(values, ip.String())
		}
	}
	return device.FilterAndSortAddresses(values)
}

func windowsEnumEquals(raw json.RawMessage, name string, number int) bool {
	value := strings.TrimSpace(string(raw))
	if value == "" || value == "null" {
		return false
	}
	if unquoted, err := strconv.Unquote(value); err == nil {
		return strings.EqualFold(strings.TrimSpace(unquoted), name)
	}
	n, err := strconv.Atoi(value)
	return err == nil && n == number
}

func containsField(fields []string, target string) bool {
	for _, field := range fields {
		if strings.EqualFold(field, target) {
			return true
		}
	}
	return false
}

func containsAnyField(fields []string, targets ...string) bool {
	for _, target := range targets {
		if containsField(fields, target) {
			return true
		}
	}
	return false
}
