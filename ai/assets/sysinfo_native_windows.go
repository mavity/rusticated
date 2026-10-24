//go:build windows && !wasip1

package assets

import (
	"syscall"
	"unsafe"
)

type nativeMemoryStatusEx struct {
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

var nativeGlobalMemoryStatusEx = syscall.NewLazyDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx")

func nativeMemorySlots() ([]MemorySlot, error) {
	status := nativeMemoryStatusEx{Length: uint32(unsafe.Sizeof(nativeMemoryStatusEx{}))}
	result, _, callErr := nativeGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&status)))
	if result == 0 {
		return nil, callErr
	}
	return setNativeRAM(status.TotalPhys, status.AvailPhys), nil
}
