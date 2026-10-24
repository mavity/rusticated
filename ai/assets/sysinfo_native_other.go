//go:build !linux && !windows && !darwin && !wasip1

package assets

func nativeMemorySlots() ([]MemorySlot, error) {
	return noNativeMemoryInfo()
}
