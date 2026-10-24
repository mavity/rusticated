package main

const (
	memorySlotCount = 16

	memKindSystemTotal  uint8 = 1
	memKindSystemAvail  uint8 = 2
	memKindGPUDiscrete  uint8 = 3
	memKindGPUShared    uint8 = 4
	memKindNPUDedicated uint8 = 5
	memKindNPUShared    uint8 = 6
	memKindSwapTotal    uint8 = 7

	memFlagUnified  uint8 = 1
	memFlagEstimate uint8 = 2
)

type hostMemorySlot struct {
	kind  uint8
	flags uint8
	bytes uint64
}

func putHostMemorySlot(slots *[memorySlotCount]hostMemorySlot, kind uint8, flags uint8, bytes uint64) {
	if bytes == 0 {
		return
	}
	for i := range slots {
		if slots[i].kind == 0 {
			slots[i] = hostMemorySlot{kind: kind, flags: flags, bytes: bytes}
			return
		}
	}
}
