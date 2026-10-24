package assets

import "fmt"

const (
	MemorySlotCount = 16

	MemKindNone         uint8 = 0
	MemKindSystemTotal  uint8 = 1
	MemKindSystemAvail  uint8 = 2
	MemKindGPUDiscrete  uint8 = 3
	MemKindGPUShared    uint8 = 4
	MemKindNPUDedicated uint8 = 5
	MemKindNPUShared    uint8 = 6
	MemKindSwapTotal    uint8 = 7

	MemoryFlagUnified  uint8 = 1
	MemoryFlagEstimate uint8 = 2
)

type MemorySlot struct {
	Kind  uint8
	Flags uint8
	Bytes uint64
}

type HardwareProfile struct {
	HostOS            string
	Slots             []MemorySlot
	TotalRAMBytes     uint64
	AvailableRAMBytes uint64
	AcceleratorBytes  uint64
	HasUnifiedMemory  bool
	WebGPUMaxBinding  uint64
	WebGPUMaxBuffer   uint64
	HasWebGPU         bool
	ConstrainedMemory bool
	RecommendedTier   string
}

func profileFromSlots(hostOS string, slots []MemorySlot) HardwareProfile {
	profile := HardwareProfile{
		HostOS: hostOS,
		Slots:  append([]MemorySlot(nil), slots...),
	}
	for _, slot := range slots {
		switch slot.Kind {
		case MemKindSystemTotal:
			if slot.Bytes > profile.TotalRAMBytes {
				profile.TotalRAMBytes = slot.Bytes
			}
		case MemKindSystemAvail:
			if slot.Bytes > profile.AvailableRAMBytes {
				profile.AvailableRAMBytes = slot.Bytes
			}
		case MemKindGPUDiscrete, MemKindGPUShared, MemKindNPUDedicated, MemKindNPUShared:
			if slot.Bytes > profile.AcceleratorBytes {
				profile.AcceleratorBytes = slot.Bytes
			}
			profile.HasUnifiedMemory = profile.HasUnifiedMemory || slot.Flags&MemoryFlagUnified != 0 || slot.Kind == MemKindGPUShared || slot.Kind == MemKindNPUShared
		}
	}
	profile.ConstrainedMemory = profile.TotalRAMBytes > 0 && profile.TotalRAMBytes <= 8<<30
	return profile
}

func recommendTier(profile HardwareProfile) string {
	for i := len(gemma4Tiers) - 1; i >= 0; i-- {
		tier := gemma4Tiers[i]
		if profile.TotalRAMBytes < tier.MinRecommendedRAMBytes {
			continue
		}
		if tier.NativeRequiresGPU && profile.AcceleratorBytes < tier.MinAcceleratorBytes {
			continue
		}
		if profile.HostOS == "browser" {
			if !tier.WebGPUSupported || !profile.HasWebGPU || profile.WebGPUMaxBuffer < uint64(tier.WebSize) {
				continue
			}
		} else if profile.AcceleratorBytes > 0 && tier.MinAcceleratorBytes > profile.AcceleratorBytes && tier.NativeRequiresGPU {
			continue
		}
		return tier.ID
	}
	return ""
}

func validateMemorySlots(slots []MemorySlot) error {
	if len(slots) > MemorySlotCount {
		return fmt.Errorf("received %d memory slots; maximum is %d", len(slots), MemorySlotCount)
	}
	for i, slot := range slots {
		if slot.Kind > MemKindSwapTotal {
			return fmt.Errorf("memory slot %d has unknown kind %d", i, slot.Kind)
		}
	}
	return nil
}
