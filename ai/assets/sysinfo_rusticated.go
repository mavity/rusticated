//go:build wasip1

package assets

import (
	"encoding/json"
	"fmt"
	"strings"
	"syscall"
)

func ProbeHardware() (HardwareProfile, error) {
	pi := syscall.GetPlatformInfo()
	if pi == nil {
		return HardwareProfile{}, fmt.Errorf("rusticated platform information is unavailable")
	}
	slots := make([]MemorySlot, 0, len(pi.MemorySlots))
	for _, slot := range pi.MemorySlots {
		if slot.Kind != MemKindNone && slot.Bytes > 0 {
			slots = append(slots, MemorySlot{Kind: slot.Kind, Flags: slot.Flags, Bytes: slot.Bytes})
		}
	}
	if err := validateMemorySlots(slots); err != nil {
		return HardwareProfile{}, err
	}
	hostOS := HostOS()
	profile := profileFromSlots(hostOS, slots)
	if hostOS == "browser" {
		if err := probeBrowserGPU(&profile); err != nil {
			return profile, err
		}
	}
	profile.RecommendedTier = recommendTier(profile)
	return profile, nil
}

func probeBrowserGPU(profile *HardwareProfile) error {
	const script = `(() => {
  return (async () => {
    const mem = navigator.deviceMemory || 0;
    if (!navigator.gpu) return JSON.stringify({ramGB: mem, webgpu: false});
    const adapter = await navigator.gpu.requestAdapter();
    return JSON.stringify({
      ramGB: mem,
      webgpu: !!adapter,
      maxBinding: adapter ? adapter.limits.maxStorageBufferBindingSize : 0,
      maxBuffer: adapter ? adapter.limits.maxBufferSize : 0
    });
  })()
})()`
	raw, err := EvalJS(script)
	if err != nil {
		return fmt.Errorf("probe browser WebGPU: %w", err)
	}
	var result struct {
		RAMGB      float64 `json:"ramGB"`
		WebGPU     bool    `json:"webgpu"`
		MaxBinding uint64  `json:"maxBinding"`
		MaxBuffer  uint64  `json:"maxBuffer"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &result); err != nil {
		return fmt.Errorf("decode browser WebGPU capabilities: %w", err)
	}
	profile.HasWebGPU = result.WebGPU
	profile.WebGPUMaxBinding = result.MaxBinding
	profile.WebGPUMaxBuffer = result.MaxBuffer
	if profile.TotalRAMBytes == 0 && result.RAMGB > 0 {
		profile.TotalRAMBytes = uint64(result.RAMGB * (1 << 30))
	}
	profile.ConstrainedMemory = profile.TotalRAMBytes > 0 && profile.TotalRAMBytes <= 8<<30
	return nil
}
