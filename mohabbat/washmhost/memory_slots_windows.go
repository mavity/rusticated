//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

type windowsMemoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

var globalMemoryStatusEx = syscall.NewLazyDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx")

func hostPlatformMemorySlots() [memorySlotCount]hostMemorySlot {
	var slots [memorySlotCount]hostMemorySlot
	status := windowsMemoryStatusEx{Length: uint32(unsafe.Sizeof(windowsMemoryStatusEx{}))}
	if result, _, _ := globalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&status))); result != 0 {
		putHostMemorySlot(&slots, memKindSystemTotal, 0, status.TotalPhys)
		putHostMemorySlot(&slots, memKindSystemAvail, 0, status.AvailPhys)
		putHostMemorySlot(&slots, memKindSwapTotal, 0, status.TotalPageFile)
	}
	return slots
}
