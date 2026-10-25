//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

func hostPlatformMemorySlots() [memorySlotCount]hostMemorySlot {
	var slots [memorySlotCount]hostMemorySlot
	var info unix.Sysinfo_t
	if unix.Sysinfo(&info) == nil {
		unit := uint64(info.Unit)
		if unit == 0 {
			unit = 1
		}
		putHostMemorySlot(&slots, memKindSystemTotal, 0, uint64(info.Totalram)*unit)
		putHostMemorySlot(&slots, memKindSystemAvail, 0, uint64(info.Freeram)*unit)
		putHostMemorySlot(&slots, memKindSwapTotal, 0, uint64(info.Totalswap)*unit)
	}

	paths, _ := filepath.Glob("/sys/class/drm/card*/device/mem_info_vram_total")
	for _, path := range paths {
		if data, err := os.ReadFile(path); err == nil {
			if bytes, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64); err == nil {
				putHostMemorySlot(&slots, memKindGPUDiscrete, 0, bytes)
			}
		}
	}
	return slots
}
