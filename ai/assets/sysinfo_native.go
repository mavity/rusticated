//go:build !wasip1

package assets

import (
	"fmt"
	"runtime"
)

// ProbeHardware gathers available native memory information without shelling out.
func ProbeHardware() (HardwareProfile, error) {
	slots, err := nativeMemorySlots()
	if err != nil {
		return HardwareProfile{}, err
	}
	if err := validateMemorySlots(slots); err != nil {
		return HardwareProfile{}, err
	}
	profile := profileFromSlots(runtime.GOOS, slots)
	profile.RecommendedTier = recommendTier(profile)
	return profile, nil
}

func setNativeRAM(total, available uint64) []MemorySlot {
	slots := make([]MemorySlot, 0, 2)
	if total > 0 {
		slots = append(slots, MemorySlot{Kind: MemKindSystemTotal, Bytes: total})
	}
	if available > 0 {
		slots = append(slots, MemorySlot{Kind: MemKindSystemAvail, Bytes: available})
	}
	if len(slots) == 0 {
		return nil
	}
	return slots
}

func noNativeMemoryInfo() ([]MemorySlot, error) {
	return nil, fmt.Errorf("native memory information is unsupported on %s", runtime.GOOS)
}
