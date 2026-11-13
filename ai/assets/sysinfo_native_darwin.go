//go:build darwin && !wasip1

package assets

import (
	"encoding/binary"
	"syscall"
)

func nativeMemorySlots() ([]MemorySlot, error) {
	raw, err := syscall.Sysctl("hw.memsize")
	if err != nil {
		return nil, err
	}
	if len(raw) != 7 && len(raw) != 8 {
		return nil, syscall.EIO
	}
	var buf [8]byte
	copy(buf[:], raw)
	total := binary.LittleEndian.Uint64(buf[:])
	slots := setNativeRAM(total, 0)
	if total > 0 {
		slots = append(slots, MemorySlot{Kind: MemKindNPUShared, Flags: MemoryFlagUnified | MemoryFlagEstimate, Bytes: total * 3 / 4})
	}
	return slots, nil
}
