//go:build darwin && !wasip1

package assets

import "syscall"

func nativeMemorySlots() ([]MemorySlot, error) {
	total, err := syscall.SysctlUint64("hw.memsize")
	if err != nil {
		return nil, err
	}
	slots := setNativeRAM(total, 0)
	if total > 0 {
		slots = append(slots, MemorySlot{Kind: MemKindNPUShared, Flags: MemoryFlagUnified | MemoryFlagEstimate, Bytes: total * 3 / 4})
	}
	return slots, nil
}
