//go:build linux && !wasip1

package assets

import "syscall"

func nativeMemorySlots() ([]MemorySlot, error) {
	var info syscall.Sysinfo_t
	if err := syscall.Sysinfo(&info); err != nil {
		return nil, err
	}
	unit := uint64(info.Unit)
	if unit == 0 {
		unit = 1
	}
	return setNativeRAM(info.Totalram*unit, info.Freeram*unit), nil
}
