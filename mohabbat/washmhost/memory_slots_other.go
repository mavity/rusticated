//go:build !linux && !windows && !darwin

package main

func hostPlatformMemorySlots() [memorySlotCount]hostMemorySlot {
	return [memorySlotCount]hostMemorySlot{}
}
