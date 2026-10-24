//go:build darwin

package main

import "golang.org/x/sys/unix"

func hostPlatformMemorySlots() [memorySlotCount]hostMemorySlot {
	var slots [memorySlotCount]hostMemorySlot
	total, err := unix.SysctlUint64("hw.memsize")
	if err == nil {
		putHostMemorySlot(&slots, memKindSystemTotal, 0, total)
		if total > 0 {
			putHostMemorySlot(&slots, memKindNPUShared, memFlagUnified|memFlagEstimate, total*3/4)
		}
	}
	return slots
}
