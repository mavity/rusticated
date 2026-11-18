//go:build linux

package platform

import (
	"os"
	"path/filepath"
	"strings"
)

func KeyboardLayoutID() string {
	if v := parseKeyValueFile("/etc/default/keyboard", "XKBLAYOUT"); v != "" {
		return v
	}
	return parseKeyValueFile("/etc/vconsole.conf", "KEYMAP")
}

func SystemLocale() string {
	return ""
}

func SystemTimeZone() string {
	if target, err := os.Readlink("/etc/localtime"); err == nil {
		if idx := strings.Index(target, "zoneinfo/"); idx >= 0 {
			if tz := strings.TrimSpace(target[idx+len("zoneinfo/"):]); tz != "" {
				return tz
			}
		}
	}
	if data, err := os.ReadFile("/etc/timezone"); err == nil {
		return strings.TrimSpace(string(data))
	}
	return ""
}

func SystemAppearance() string {
	return ""
}

func PowerSource() string {
	const powerSupplyDir = "/sys/class/power_supply"
	entries, err := os.ReadDir(powerSupplyDir)
	if err != nil {
		return ""
	}
	hasBattery := false
	for _, entry := range entries {
		base := filepath.Join(powerSupplyDir, entry.Name())
		kindBytes, err := os.ReadFile(filepath.Join(base, "type"))
		if err != nil {
			continue
		}
		kind := strings.TrimSpace(string(kindBytes))
		switch kind {
		case "Mains":
			onlineBytes, err := os.ReadFile(filepath.Join(base, "online"))
			if err == nil && strings.TrimSpace(string(onlineBytes)) == "1" {
				return "ac"
			}
		case "Battery":
			hasBattery = true
		}
	}
	if hasBattery {
		return "battery"
	}
	return ""
}

func parseKeyValueFile(path, key string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	prefix := key + "="
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, prefix) {
			val := strings.TrimSpace(strings.TrimPrefix(line, prefix))
			val = strings.Trim(val, `"'`)
			return strings.TrimSpace(val)
		}
	}
	return ""
}
